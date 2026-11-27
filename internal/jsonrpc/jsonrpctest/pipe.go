// Package jsonrpctest helps test code built on internal/jsonrpc: two
// connected Conns in one process (Pipe), a Conn facing raw frames (Wire),
// and the test binary itself as a child process (RunChild, ChildSpec).
package jsonrpctest

import (
	"bufio"
	"io"
	"testing"

	"github.com/ryanhelms/agentflow/internal/jsonrpc"
)

// Pipe returns two connected Conns: a answers with ha, b with hb. Both are
// closed when the test ends.
func Pipe(tb testing.TB, ha, hb jsonrpc.Handler, l jsonrpc.Limits) (a, b jsonrpc.Conn) {
	tb.Helper()
	aR, bW := io.Pipe() // b writes, a reads
	bR, aW := io.Pipe() // a writes, b reads
	a = jsonrpc.NewConn(aR, aW, ha, l)
	b = jsonrpc.NewConn(bR, bW, hb, l)
	tb.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})
	return a, b
}

// Peer is the far end of a Conn under test, reading and writing raw frames.
type Peer struct {
	// In carries what the Conn writes, one frame per line.
	In *bufio.Reader
	// Out is what the Conn reads.
	Out io.WriteCloser
}

// ReadLine returns the next frame the Conn wrote, without its newline.
func (p *Peer) ReadLine() (string, error) {
	line, err := p.In.ReadString('\n')
	if err != nil {
		return line, err
	}
	return line[:len(line)-1], nil
}

// WriteLine sends s and a newline to the Conn.
func (p *Peer) WriteLine(s string) error {
	_, err := io.WriteString(p.Out, s+"\n")
	return err
}

// Wire returns a Conn answering with h and the raw Peer it talks to, for
// tests of framing and protocol errors. Both close when the test ends.
func Wire(tb testing.TB, h jsonrpc.Handler, l jsonrpc.Limits) (jsonrpc.Conn, *Peer) {
	tb.Helper()
	cR, pW := io.Pipe()
	pR, cW := io.Pipe()
	c := jsonrpc.NewConn(cR, cW, h, l)
	tb.Cleanup(func() {
		_ = c.Close()
		_ = pW.Close()
		_ = pR.Close()
	})
	return c, &Peer{In: bufio.NewReaderSize(pR, 64<<10), Out: pW}
}
