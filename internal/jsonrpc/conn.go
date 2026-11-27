package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"
)

// notifyQueue is how many incoming notifications wait for the Handler
// before the reader stops reading (back-pressure on the peer).
const notifyQueue = 64

// errWriteTimeout closes a connection whose peer stopped reading.
var errWriteTimeout = fmt.Errorf("%w: write timed out", ErrClosed)

// NewConn starts a JSON-RPC 2.0 connection that reads frames from r and
// writes them to w. It starts reading at once. h may be nil: every request
// then gets CodeMethodNotFound and notifications are dropped.
//
// Requests from the peer run on their own goroutine, at most
// Limits.MaxInFlight at once; one more is answered CodeBusy. Notifications
// are not counted in MaxInFlight: they run in arrival order on one
// goroutine, and when notifyQueue of them wait the reader stops reading
// until the Handler catches up. A notification Handler must therefore not
// Call on the same Conn.
//
// With Limits.OrderedResponses, a response joins the notification queue
// instead of reaching its Call at once, so Call returns only after every
// notification read before the response has been handled. The response's
// ID is still checked as it is read (an unknown ID closes the connection at
// once). At EOF the queue is handled before the connection ends, so a
// response or notification the peer sent just before it exited is not lost.
//
// If r is also an io.Closer, Close and every fatal error close it, so a
// blocked read ends.
func NewConn(r io.Reader, w io.WriteCloser, h Handler, l Limits) Conn {
	l = l.withDefaults()
	ctx, cancel := context.WithCancel(context.Background())
	c := &conn{
		r:       r,
		fr:      newFrameReader(r, l.MaxFrame),
		w:       w,
		h:       h,
		l:       l,
		ctx:     ctx,
		cancel:  cancel,
		pending: map[int64]chan *Message{},
		slots:   make(chan struct{}, l.MaxInFlight),
		notes:   make(chan queued, notifyQueue),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	go c.notifyLoop()
	return c
}

func (l Limits) withDefaults() Limits {
	if l.MaxFrame <= 0 {
		l.MaxFrame = DefaultMaxFrame
	}
	if l.MaxInFlight <= 0 {
		l.MaxInFlight = DefaultMaxInFlight
	}
	if l.CallTimeout <= 0 {
		l.CallTimeout = DefaultCallTimeout
	}
	if l.WriteTimeout <= 0 {
		l.WriteTimeout = DefaultWriteTimeout
	}
	return l
}

type conn struct {
	r  io.Reader
	fr *frameReader
	w  io.WriteCloser
	h  Handler
	l  Limits

	ctx    context.Context // handlers' context; ends when the connection does
	cancel context.CancelFunc

	wmu sync.Mutex // one frame at a time

	mu      sync.Mutex
	lastID  int64
	pending map[int64]chan *Message

	slots chan struct{} // peer requests in flight
	notes chan queued   // peer notifications (and ordered responses) waiting

	once sync.Once
	done chan struct{}
	err  error // set before done closes
}

// queued is one item of the notification queue: a notification, a
// response (OrderedResponses only) or the end of input (OrderedResponses
// only, at EOF).
type queued struct {
	note *Request
	resp *Message
	eof  bool
}

func (c *conn) Done() <-chan struct{} { return c.done }

func (c *conn) Err() error {
	select {
	case <-c.done:
		return c.err
	default:
		return nil
	}
}

func (c *conn) Close() error {
	c.fail(nil)
	return nil
}

// fail ends the connection once, recording why.
func (c *conn) fail(err error) {
	c.once.Do(func() {
		c.err = err
		close(c.done)
		c.cancel()
		_ = c.w.Close()
		if rc, ok := c.r.(io.Closer); ok {
			_ = rc.Close()
		}
	})
}

func (c *conn) Call(ctx context.Context, method string, params, result any) error {
	if method == "" {
		return errors.New("jsonrpc: empty method")
	}
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.l.CallTimeout)
		defer cancel()
	}

	ch := make(chan *Message, 1)
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return ErrClosed
	default:
	}
	c.lastID++
	id := c.lastID
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	msg := &Message{JSONRPC: Version, ID: ID(strconv.FormatInt(id, 10)), Method: method, Params: raw}
	if err := c.write(msg); err != nil {
		return err
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			e := *resp.Error
			return &e
		}
		if result == nil {
			return nil
		}
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fmt.Errorf("jsonrpc: decode %s result: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("jsonrpc: call %s: %w", method, ctx.Err())
	case <-c.done:
		return ErrClosed
	}
}

func (c *conn) Notify(_ context.Context, method string, params any) error {
	if method == "" {
		return errors.New("jsonrpc: empty method")
	}
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	return c.write(&Message{JSONRPC: Version, Method: method, Params: raw})
}

func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("jsonrpc: encode params: %w", err)
	}
	if isNull(raw) {
		return nil, nil
	}
	if !structured(raw) {
		return nil, errors.New("jsonrpc: params must be an object or an array")
	}
	return raw, nil
}

// write sends one frame atomically. A frame over MaxFrame is refused
// without closing; a failed or timed-out write closes the connection, since
// a partly written frame would corrupt the stream.
func (c *conn) write(m *Message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("jsonrpc: encode: %w", err)
	}
	if len(b) > c.l.MaxFrame {
		return ErrFrameTooLarge
	}
	b = append(b, '\n')

	c.wmu.Lock()
	defer c.wmu.Unlock()
	select {
	case <-c.done:
		return ErrClosed
	default:
	}
	timer := time.AfterFunc(c.l.WriteTimeout, func() { c.fail(errWriteTimeout) })
	_, err = c.w.Write(b)
	stopped := timer.Stop()
	if err != nil {
		if !stopped {
			return errWriteTimeout
		}
		c.fail(fmt.Errorf("%w: write: %w", ErrClosed, err))
		return ErrClosed
	}
	return nil
}

func (c *conn) readLoop() {
	defer close(c.notes)
	for {
		line, err := c.fr.next()
		if err != nil {
			switch {
			case errors.Is(err, ErrFrameTooLarge):
				c.fail(ErrFrameTooLarge)
			case errors.Is(err, io.EOF) && c.l.OrderedResponses:
				// Handle what is queued first; notifyLoop then ends the
				// connection.
				select {
				case c.notes <- queued{eof: true}:
				case <-c.done:
				}
			case errors.Is(err, io.EOF):
				c.fail(io.EOF)
			default:
				// A read error after Close keeps Close's nil reason.
				c.fail(fmt.Errorf("%w: read: %w", ErrClosed, err))
			}
			return
		}
		m, kind, perr := decodeFrame(line)
		if perr != nil {
			var id ID
			if m != nil && m.Method != "" && validID(m.ID) {
				id = m.ID
			}
			_ = c.respond(id, nil, perr)
			c.fail(ErrProtocol)
			return
		}
		switch kind {
		case kindResponse:
			if c.l.OrderedResponses {
				if _, ok := c.knownID(m); !ok {
					c.fail(ErrProtocol)
					return
				}
				select {
				case c.notes <- queued{resp: m}:
				case <-c.done:
					return
				}
				continue
			}
			if !c.deliver(m) {
				c.fail(ErrProtocol)
				return
			}
		case kindNotification:
			r := &Request{Method: m.Method, Params: m.Params}
			select {
			case c.notes <- queued{note: r}:
			case <-c.done:
				return
			}
		case kindRequest:
			c.dispatch(&Request{ID: m.ID, Method: m.Method, Params: m.Params})
		}
	}
}

// deliver hands a response to its Call. It reports false for an ID this
// side never sent; a late answer to an abandoned Call is dropped.
func (c *conn) deliver(m *Message) bool {
	id, ok := c.knownID(m)
	if !ok {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch, ok := c.pending[id]; ok {
		delete(c.pending, id)
		ch <- m
	}
	return true
}

// knownID parses a response's ID and reports whether this side sent it.
func (c *conn) knownID(m *Message) (int64, bool) {
	id, err := strconv.ParseInt(string(bytes.TrimSpace(m.ID)), 10, 64)
	if err != nil {
		return 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return id, id > 0 && id <= c.lastID
}

func (c *conn) dispatch(r *Request) {
	select {
	case c.slots <- struct{}{}:
	default:
		_ = c.respond(r.ID, nil, newError(CodeBusy, "busy", "busy"))
		return
	}
	go func() {
		defer func() { <-c.slots }()
		result, err := c.handle(r)
		_ = c.respond(r.ID, result, err)
	}()
}

func (c *conn) notifyLoop() {
	for q := range c.notes {
		switch {
		case q.eof:
			c.fail(io.EOF)
			continue
		case q.resp != nil:
			c.deliver(q.resp) // its ID was checked when it was read
			continue
		}
		if c.h == nil {
			continue
		}
		select {
		case <-c.done:
			continue // drain without handling
		default:
		}
		c.handleNote(q.note)
	}
}

// handleNote runs the Handler for one notification; a panic is dropped
// with it.
func (c *conn) handleNote(r *Request) {
	defer func() { _ = recover() }()
	_, _ = c.h.Handle(c.ctx, r)
}

// handle runs the Handler and maps its error to what the peer may see.
//
// A panic in the Handler is answered as an internal error and never ends
// the process.
func (c *conn) handle(r *Request) (result any, rerr *Error) {
	if c.h == nil {
		return nil, newError(CodeMethodNotFound, "method not found", "method_not_found")
	}
	defer func() {
		if p := recover(); p != nil {
			result, rerr = nil, internalError()
		}
	}()
	result, err := c.h.Handle(c.ctx, r)
	if err != nil {
		var e *Error
		if errors.As(err, &e) && e != nil {
			return nil, e
		}
		return nil, internalError()
	}
	return result, nil
}

func internalError() *Error {
	return newError(CodeInternalError, "internal error", "internal")
}

// respond answers a request. id may be nil (JSON null) when the request's
// ID could not be read.
func (c *conn) respond(id ID, result any, rerr *Error) error {
	if len(id) == 0 {
		id = ID("null")
	}
	m := &Message{JSONRPC: Version, ID: id}
	if rerr != nil {
		m.Error = rerr
	} else {
		raw, err := json.Marshal(result)
		if err != nil {
			m.Error = internalError()
		} else {
			m.Result = raw
		}
	}
	err := c.write(m)
	if errors.Is(err, ErrFrameTooLarge) && m.Error == nil {
		m.Result, m.Error = nil, internalError()
		err = c.write(m)
	}
	return err
}
