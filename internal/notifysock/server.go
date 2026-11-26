package notifysock

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"time"
)

// peerUID returns the user ID of the process at the other end of a Unix
// socket connection. reported is false where the platform does not say
// (other transports, or a Unix without peer credentials). Tests replace it.
var peerUID = socketPeerUID

// ownUID is the user ID a Unix peer must have. Tests replace it.
var ownUID = os.Geteuid

// Serve accepts connections on l until ctx ends or l is closed, and hands
// each authenticated publish Request to a. instanceKey is the daemon
// instance's registry key (decoded); Serve derives the socket key from it.
// Serve applies Check to every event before calling a, so an Acceptor only
// adds its ownership checks. It closes l and every connection before it
// returns, and returns nil when ctx ended or l was closed.
func Serve(ctx context.Context, l net.Listener, instanceKey []byte, a Acceptor) error {
	s := &server{
		key:   DeriveKey(instanceKey),
		a:     a,
		slots: make(chan struct{}, MaxConnections),
		busy:  make(chan struct{}, MaxConnections),
		conns: map[net.Conn]struct{}{},
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = l.Close() })
	defer stop()
	var err error
	for {
		var c net.Conn
		c, err = l.Accept()
		if err != nil {
			var ne net.Error
			if ctx.Err() == nil && errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			break
		}
		s.dispatch(ctx, c)
	}
	_ = l.Close()
	cancel()
	s.closeAll()
	s.wg.Wait()
	if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

type server struct {
	key   []byte
	a     Acceptor
	slots chan struct{} // one per served connection
	busy  chan struct{} // one per connection being refused with ErrorBusy
	wg    sync.WaitGroup

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
}

// dispatch serves c in a slot, refuses it with ErrorBusy when every slot is
// taken, or closes it when even the refusals are saturated.
func (s *server) dispatch(ctx context.Context, c net.Conn) {
	var release chan struct{}
	serve := s.serve
	select {
	case s.slots <- struct{}{}:
		release = s.slots
	default:
		select {
		case s.busy <- struct{}{}:
			release, serve = s.busy, s.refuseBusy
		default:
			_ = c.Close()
			return
		}
	}
	if !s.track(c) {
		<-release
		_ = c.Close()
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { <-release }()
		defer s.untrack(c)
		serve(ctx, c)
	}()
}

func (s *server) track(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[c] = struct{}{}
	return true
}

func (s *server) untrack(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
	_ = c.Close()
}

func (s *server) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for c := range s.conns {
		_ = c.Close()
	}
}

// refuseBusy reads the Hello and answers it with ErrorBusy.
func (s *server) refuseBusy(_ context.Context, c net.Conn) {
	deadline := time.Now().Add(HandshakeTimeout)
	_ = c.SetDeadline(deadline)
	var hello Hello
	if readJSON(bufio.NewReader(c), &hello) != nil {
		return
	}
	_ = writeJSON(c, deadline, Challenge{V: Version, Error: &FrameError{Code: ErrorBusy, Message: "too many connections"}})
}

// handshake runs the daemon's half: Hello, Challenge, Proof, Ready. It
// reports whether the client proved the key (and, on Unix, its user ID).
func (s *server) handshake(c net.Conn, r *bufio.Reader) bool {
	deadline := time.Now().Add(HandshakeTimeout)
	if c.SetDeadline(deadline) != nil {
		return false
	}
	var hello Hello
	if readJSON(r, &hello) != nil {
		return false
	}
	if hello.V != Version {
		_ = writeJSON(c, deadline, Challenge{V: Version, Error: &FrameError{Code: ErrorVersion, Message: "unsupported protocol version"}})
		return false
	}
	if !validNonce(hello.Nonce) {
		_ = writeJSON(c, deadline, Challenge{V: Version, Error: &FrameError{Code: ErrorFrame, Message: "invalid nonce"}})
		return false
	}
	sn := newNonce()
	if writeJSON(c, deadline, Challenge{V: Version, Nonce: sn, Proof: ServerProof(s.key, hello.Nonce, sn)}) != nil {
		return false
	}
	var p Proof
	if readJSON(r, &p) != nil {
		return false
	}
	if !VerifyProof(ClientProof(s.key, sn, hello.Nonce), p.Proof) || !peerAllowed(c) {
		_ = writeJSON(c, deadline, Ready{Error: &FrameError{Code: ErrorAuth, Message: "authentication failed"}})
		return false
	}
	return writeJSON(c, deadline, Ready{OK: true}) == nil && c.SetDeadline(time.Time{}) == nil
}

// peerAllowed applies the peer user ID check where the platform reports one.
func peerAllowed(c net.Conn) bool {
	uid, reported, err := peerUID(c)
	if err != nil {
		return false
	}
	return !reported || uid == ownUID()
}

func (s *server) serve(ctx context.Context, c net.Conn) {
	r := bufio.NewReaderSize(c, 64<<10)
	if !s.handshake(c, r) {
		return
	}
	for ctx.Err() == nil {
		if c.SetReadDeadline(time.Now().Add(IdleTimeout)) != nil {
			return
		}
		var req Request
		if err := readJSON(r, &req); err != nil {
			if errors.Is(err, errFrameTooLarge) || errors.Is(err, errMalformed) {
				_ = writeJSON(c, time.Time{}, Response{ID: req.ID, Error: &FrameError{Code: ErrorFrame, Message: err.Error()}})
			}
			return
		}
		resp, keep := s.handle(ctx, req)
		if writeJSON(c, time.Time{}, resp) != nil || !keep {
			return
		}
	}
}

// handle answers one Request. keep is false when the connection must close.
func (s *server) handle(ctx context.Context, req Request) (resp Response, keep bool) {
	resp.ID = req.ID
	refuse := func(code, msg string) (Response, bool) {
		resp.Error = &FrameError{Code: code, Message: msg}
		return resp, code != ErrorFrame
	}
	switch req.Op {
	case OpPing:
		if req.Event != nil {
			return refuse(ErrorFrame, "ping carries no event")
		}
		resp.OK = true
		return resp, true
	case OpPublish:
	default:
		return refuse(ErrorFrame, "unknown op")
	}
	if req.Event == nil {
		return refuse(ErrorInvalidEvent, "publish needs an event")
	}
	if code := Check(*req.Event); code != "" {
		return refuse(code, "")
	}
	ctx, cancel := context.WithTimeout(ctx, SendTimeout)
	defer cancel()
	e, err := s.a.Accept(ctx, *req.Event)
	if err != nil {
		code := codeOf(err)
		msg := ""
		if code != ErrorInternal {
			var r *Rejection
			if errors.As(err, &r) {
				msg = r.Message
			}
		}
		return refuse(code, msg)
	}
	resp.OK, resp.Seq = true, e.Seq
	return resp, true
}
