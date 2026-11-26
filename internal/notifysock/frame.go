package notifysock

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// Rejection is the error for a refusal with a FrameError code. It matches
// ErrRejected with errors.Is. Acceptors return it (Reject) so Serve can
// answer with the right code; Client.Send returns it for an error Response.
type Rejection struct {
	Code    string
	Message string
}

func (r *Rejection) Error() string {
	if r.Message == "" {
		return fmt.Sprintf("%v: %s", ErrRejected, r.Code)
	}
	return fmt.Sprintf("%v: %s: %s", ErrRejected, r.Code, r.Message)
}

// Is makes a Rejection match ErrRejected.
func (r *Rejection) Is(target error) bool { return target == ErrRejected }

// Reject returns a Rejection with code and message.
func Reject(code, message string) error { return &Rejection{Code: code, Message: message} }

// errorCodes are the codes an Acceptor may name; codeOf finds them in an
// error that wraps ErrRejected without being a Rejection.
var errorCodes = []string{ErrorInvalidEvent, ErrorForbiddenType, ErrorForbiddenSource, ErrorUnknownRepo, ErrorUnknownWorktree, ErrorInternal}

// codeOf maps an Acceptor error to a FrameError code. Anything that is not
// a refusal is ErrorInternal.
func codeOf(err error) string {
	var r *Rejection
	if errors.As(err, &r) {
		return r.Code
	}
	if errors.Is(err, ErrRejected) {
		msg := err.Error()
		for _, code := range errorCodes {
			if strings.HasSuffix(msg, ": "+code) || strings.Contains(msg, ": "+code+":") {
				return code
			}
		}
	}
	return ErrorInternal
}

var errFrameTooLarge = errors.New("notify frame exceeds the size limit")

// readFrame reads one newline-terminated frame of at most MaxFrame bytes,
// newline included.
func readFrame(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(buf)+len(chunk) > MaxFrame {
			return nil, errFrameTooLarge
		}
		buf = append(buf, chunk...)
		switch {
		case err == nil:
			return buf, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(buf) > 0:
			return nil, io.ErrUnexpectedEOF
		default:
			return nil, err
		}
	}
}

// readJSON reads one frame into v.
func readJSON(r *bufio.Reader, v any) error {
	line, err := readFrame(r)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(line, v); err != nil {
		return fmt.Errorf("%w: %w", errMalformed, err)
	}
	return nil
}

var errMalformed = errors.New("malformed notify frame")

// writeJSON writes v as one frame, bounded by WriteTimeout or the earlier
// deadline.
func writeJSON(c net.Conn, deadline time.Time, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if len(raw) > MaxFrame {
		return errFrameTooLarge
	}
	if limit := time.Now().Add(WriteTimeout); deadline.IsZero() || limit.Before(deadline) {
		deadline = limit
	}
	if err := c.SetWriteDeadline(deadline); err != nil {
		return err
	}
	_, err = c.Write(raw)
	return err
}

// newNonce returns NonceBytes random bytes in unpadded base64url.
func newNonce() string {
	b := make([]byte, NonceBytes)
	_, _ = rand.Read(b) // Since Go 1.24, crypto/rand.Read never returns an error.
	return base64.RawURLEncoding.EncodeToString(b)
}

// validNonce reports whether s is NonceBytes of unpadded base64url.
func validNonce(s string) bool {
	b, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil && len(b) == NonceBytes
}
