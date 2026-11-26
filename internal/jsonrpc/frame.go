package jsonrpc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// frameReader reads newline-terminated frames and refuses one longer than
// max bytes (newline excluded) without buffering more than max plus one
// bufio block.
type frameReader struct {
	br  *bufio.Reader
	max int
	buf []byte
}

func newFrameReader(r io.Reader, maxFrame int) *frameReader {
	size := 64 << 10
	if maxFrame < size {
		size = maxFrame + 1
	}
	if size < 16 {
		size = 16
	}
	return &frameReader{br: bufio.NewReaderSize(r, size), max: maxFrame}
}

// next returns the next non-blank frame without its line ending. The slice
// is valid until the next call. It returns io.EOF at the end of the stream
// (a final frame without a newline is dropped) and ErrFrameTooLarge for a
// frame over the limit.
func (f *frameReader) next() ([]byte, error) {
	for {
		f.buf = f.buf[:0]
		for {
			chunk, err := f.br.ReadSlice('\n')
			if len(f.buf)+len(chunk) > f.max+2 { // room for "\r\n"
				return nil, ErrFrameTooLarge
			}
			f.buf = append(f.buf, chunk...)
			if err == nil {
				break
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if errors.Is(err, io.EOF) {
				return nil, io.EOF
			}
			return nil, err
		}
		line := bytes.TrimSuffix(f.buf, []byte("\n"))
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) > f.max {
			return nil, ErrFrameTooLarge
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		return line, nil
	}
}

// frameKind is what a valid frame is.
type frameKind int

const (
	kindRequest frameKind = iota + 1
	kindNotification
	kindResponse
)

// decodeFrame parses one frame. A frame that is not JSON is CodeParseError;
// valid JSON that is not exactly one JSON-RPC 2.0 shape is
// CodeInvalidRequest. The returned *Error is what to answer the peer.
func decodeFrame(line []byte) (*Message, frameKind, *Error) {
	if !json.Valid(line) {
		return nil, 0, newError(CodeParseError, "parse error", "parse_error")
	}
	var m Message
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, 0, invalidRequest()
	}
	if m.JSONRPC != Version {
		return &m, 0, invalidRequest()
	}
	hasID := len(m.ID) > 0
	if hasID && !validID(m.ID) && (!isNull(m.ID) || m.Method != "") {
		return &m, 0, invalidRequest()
	}
	if m.Method != "" {
		if len(m.Result) > 0 || m.Error != nil {
			return &m, 0, invalidRequest()
		}
		if len(m.Params) > 0 && !structured(m.Params) {
			return &m, 0, invalidRequest()
		}
		if !hasID {
			return &m, kindNotification, nil
		}
		if isNull(m.ID) {
			return &m, 0, invalidRequest()
		}
		return &m, kindRequest, nil
	}
	if !hasID || len(m.Params) > 0 || (len(m.Result) > 0) == (m.Error != nil) {
		return &m, 0, invalidRequest()
	}
	return &m, kindResponse, nil
}

func invalidRequest() *Error {
	return newError(CodeInvalidRequest, "invalid request", "invalid_request")
}

// validID reports whether raw is a JSON string or number.
func validID(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return false
	}
	switch c := raw[0]; {
	case c == '"':
		return true
	case c == '-' || (c >= '0' && c <= '9'):
		return true
	}
	return false
}

func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// structured reports whether params are an object or an array.
func structured(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && (raw[0] == '{' || raw[0] == '[')
}

// newError builds an *Error with Data {"code": code}.
func newError(code int, message, dataCode string) *Error {
	e := &Error{Code: code, Message: message}
	if dataCode != "" {
		e.Data, _ = json.Marshal(ErrorData{Code: dataCode})
	}
	return e
}
