package notifysock

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// Client is a producer's connection to the daemon. It keeps one
// connection, pipelines up to MaxInFlight requests on it, and dials again
// when the daemon closed an idle one. It is safe for concurrent use.
type Client struct {
	loc Locator

	mu     sync.Mutex
	conn   *clientConn
	closed bool
}

// Dial locates the daemon and connects to it, running the handshake. It
// returns an error wrapping ErrNoDaemon when no daemon is registered,
// nothing listens, or the listener cannot prove it knows the instance key
// (a stale or foreign listener, which then has learned only a random
// nonce). A daemon that refuses the connection (ErrorBusy, ErrorVersion,
// ErrorAuth) gives an error wrapping ErrRejected.
func Dial(ctx context.Context, loc Locator) (*Client, error) {
	c := &Client{loc: loc}
	cc, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	c.conn = cc
	return c, nil
}

// Close closes the connection. Sends in flight fail with ErrNoDaemon.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.conn != nil {
		c.conn.fail(net.ErrClosed)
		c.conn = nil
	}
	return nil
}

// Send implements Sender. Without a deadline on ctx it waits at most
// SendTimeout.
func (c *Client) Send(ctx context.Context, e bus.Event) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, SendTimeout)
		defer cancel()
	}
	cc, err := c.connection(ctx)
	if err != nil {
		return err
	}
	resp, err := cc.roundTrip(ctx, Request{Op: OpPublish, Event: &e})
	if err != nil {
		return err
	}
	if !resp.OK {
		if resp.Error == nil {
			return &Rejection{Code: ErrorInternal}
		}
		return &Rejection{Code: resp.Error.Code, Message: resp.Error.Message}
	}
	return nil
}

// Ping checks the connection.
func (c *Client) Ping(ctx context.Context) error {
	cc, err := c.connection(ctx)
	if err != nil {
		return err
	}
	resp, err := cc.roundTrip(ctx, Request{Op: OpPing})
	if err == nil && !resp.OK {
		err = &Rejection{Code: ErrorInternal}
	}
	return err
}

// connection returns the live connection, dialing again when the daemon
// closed the last one.
func (c *Client) connection(ctx context.Context) (*clientConn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, fmt.Errorf("%w: client closed", ErrNoDaemon)
	}
	if c.conn != nil && !c.conn.isDead() {
		return c.conn, nil
	}
	cc, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	c.conn = cc
	return cc, nil
}

func noDaemon(err error) error {
	if errors.Is(err, ErrNoDaemon) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrNoDaemon, err)
}

func (c *Client) dial(ctx context.Context) (*clientConn, error) {
	if c.loc == nil {
		return nil, fmt.Errorf("%w: no locator", ErrNoDaemon)
	}
	ep, err := c.loc(ctx)
	if err != nil {
		return nil, noDaemon(err)
	}
	if len(ep.Key) == 0 {
		return nil, fmt.Errorf("%w: endpoint has no key", ErrNoDaemon)
	}
	d := net.Dialer{Timeout: DialTimeout}
	conn, err := d.DialContext(ctx, ep.Network, ep.Address)
	if err != nil {
		return nil, noDaemon(err)
	}
	r := bufio.NewReaderSize(conn, 64<<10)
	if err := clientHandshake(ctx, conn, r, DeriveKey(ep.Key)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	cc := &clientConn{c: conn, slots: make(chan struct{}, MaxInFlight), dead: make(chan struct{})}
	go cc.readLoop(r)
	return cc, nil
}

// clientHandshake runs the producer's half of the handshake.
func clientHandshake(ctx context.Context, conn net.Conn, r *bufio.Reader, key []byte) error {
	deadline := time.Now().Add(HandshakeTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return noDaemon(err)
	}
	cn := newNonce()
	if err := writeJSON(conn, deadline, Hello{V: Version, Nonce: cn}); err != nil {
		return noDaemon(err)
	}
	var ch Challenge
	if err := readJSON(r, &ch); err != nil {
		return noDaemon(err)
	}
	if ch.Error != nil {
		return &Rejection{Code: ch.Error.Code, Message: ch.Error.Message}
	}
	if ch.V != Version || !validNonce(ch.Nonce) || !VerifyProof(ServerProof(key, cn, ch.Nonce), ch.Proof) {
		// A stale or foreign listener: it learned only our nonce.
		return fmt.Errorf("%w: the listener did not prove the instance key", ErrNoDaemon)
	}
	if err := writeJSON(conn, deadline, Proof{Proof: ClientProof(key, ch.Nonce, cn)}); err != nil {
		return noDaemon(err)
	}
	var ready Ready
	if err := readJSON(r, &ready); err != nil {
		return noDaemon(err)
	}
	if !ready.OK {
		code := ErrorAuth
		if ready.Error != nil {
			code = ready.Error.Code
		}
		return &Rejection{Code: code}
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return noDaemon(err)
	}
	return nil
}

// clientConn is one authenticated connection.
type clientConn struct {
	c     net.Conn
	slots chan struct{} // requests in flight, at most MaxInFlight

	wmu    sync.Mutex // serializes writes and the order of pending
	nextID uint64

	mu      sync.Mutex
	pending []*call
	err     error
	dead    chan struct{}
}

type call struct {
	id   uint64
	done chan Response // buffered: the reader never blocks on a gone caller
}

func (cc *clientConn) isDead() bool {
	select {
	case <-cc.dead:
		return true
	default:
		return false
	}
}

// fail closes the connection and fails every pending call.
func (cc *clientConn) fail(err error) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if cc.err != nil {
		return
	}
	cc.err = err
	close(cc.dead)
	_ = cc.c.Close()
	for range cc.pending {
		<-cc.slots
	}
	cc.pending = nil
}

func (cc *clientConn) roundTrip(ctx context.Context, req Request) (Response, error) {
	select {
	case cc.slots <- struct{}{}:
	case <-cc.dead:
		return Response{}, noDaemon(cc.reason())
	case <-ctx.Done():
		return Response{}, ctx.Err()
	}
	cl := &call{done: make(chan Response, 1)}
	cc.wmu.Lock()
	cc.nextID++
	cl.id, req.ID = cc.nextID, cc.nextID
	cc.mu.Lock()
	if cc.err != nil {
		cc.mu.Unlock()
		cc.wmu.Unlock()
		<-cc.slots
		return Response{}, noDaemon(cc.err)
	}
	cc.pending = append(cc.pending, cl)
	cc.mu.Unlock()
	deadline, _ := ctx.Deadline()
	err := writeJSON(cc.c, deadline, req)
	cc.wmu.Unlock()
	if err != nil {
		cc.fail(err)
		return Response{}, noDaemon(err)
	}
	select {
	case resp := <-cl.done:
		return resp, nil
	case <-cc.dead:
		select {
		case resp := <-cl.done:
			return resp, nil
		default:
		}
		return Response{}, noDaemon(cc.reason())
	case <-ctx.Done():
		return Response{}, ctx.Err()
	}
}

func (cc *clientConn) reason() error {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if cc.err == nil {
		return errors.New("connection closed")
	}
	return cc.err
}

// readLoop matches responses to pending calls in order.
func (cc *clientConn) readLoop(r *bufio.Reader) {
	for {
		var resp Response
		if err := readJSON(r, &resp); err != nil {
			cc.fail(err)
			return
		}
		cc.mu.Lock()
		if len(cc.pending) == 0 || cc.pending[0].id != resp.ID {
			cc.mu.Unlock()
			cc.fail(fmt.Errorf("response %d out of order", resp.ID))
			return
		}
		cl := cc.pending[0]
		cc.pending = cc.pending[1:]
		<-cc.slots
		cc.mu.Unlock()
		cl.done <- resp
		if resp.Error != nil && resp.Error.Code == ErrorFrame {
			cc.fail(errors.New("daemon closed the connection after a frame error"))
			return
		}
	}
}
