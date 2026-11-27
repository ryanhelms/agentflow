// timeline-hints is a sample agentflow plugin: a small program that adds a
// hint to the console timeline when an operation fails.
//
// It speaks agentflow's plugin protocol directly, so you can see all of it.
// The protocol is JSON-RPC 2.0, one JSON object per line, on standard input
// and output. The host sends requests (initialize, event.deliver, ping,
// shutdown) and this program answers them. After it answers initialize, it
// sends two requests of its own: subscribe, to choose what it hears about,
// and event.publish, to add a note.
//
// Everything here is standard library. Build it before you add the plugin:
//
//	go build -o timeline-hints .
//
// The program runs as the user who runs agentflow. agentflow gives it a clean
// environment, but that is not a sandbox. Read this file before you add it.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// protocolVersion is the only version of the plugin protocol.
const protocolVersion = 1

// hints says what to do next for each failure code of op.failed. A code that
// is not here gets no hint.
var hints = map[string]string{
	"operation_changed": "The candidate changed after review. Prepare it again.",
	"approval_expired":  "The approval ran out. Ask for a fresh preview.",
	"approval_consumed": "That approval was already used. Check the result before you retry.",
	"approval_pending":  "A person has not approved this yet. Approve it in the console, or with agentflow policy approve.",
	"invalid_token":     "The confirmation token is not valid. Ask for a fresh preview.",
	"blocked":           "A rule blocked this. Run agentflow policy explain to see which.",
}

// message is one JSON-RPC frame. A request has Method and ID, a notification
// has Method only, and an answer has ID and Result or Error.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func main() {
	if err := serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "timeline-hints:", err)
		os.Exit(1)
	}
}

// serve reads frames until the host closes standard input.
func serve(in io.Reader, out io.Writer) error {
	r := bufio.NewReaderSize(in, 1<<20)
	enc := json.NewEncoder(out) // Encode writes one line per frame
	nextID := 0
	request := func(method string, params any) error {
		nextID++
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		return enc.Encode(message{JSONRPC: "2.0", ID: json.RawMessage(fmt.Sprint(nextID)), Method: method, Params: raw})
	}
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var m message
			if jsonErr := json.Unmarshal(line, &m); jsonErr != nil {
				return fmt.Errorf("not a JSON frame: %w", jsonErr)
			}
			if herr := handle(m, enc, request); herr != nil {
				return herr
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// handle answers one frame from the host.
func handle(m message, enc *json.Encoder, request func(string, any) error) error {
	answer := func(result any) error {
		return enc.Encode(message{JSONRPC: "2.0", ID: m.ID, Result: result})
	}
	switch m.Method {
	case "":
		return nil // an answer to one of our requests: nothing to do
	case "initialize":
		if err := answer(map[string]int{"protocol_version": protocolVersion}); err != nil {
			return err
		}
		return request("subscribe", map[string][]string{"types": {"op.failed"}})
	case "event.deliver":
		var p struct {
			Event struct {
				ID            string `json:"id"`
				CorrelationID string `json:"correlation_id"`
				Payload       struct {
					Code string `json:"code"`
				} `json:"payload"`
			} `json:"event"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return enc.Encode(message{JSONRPC: "2.0", ID: m.ID, Error: &rpcError{Code: -32602, Message: "invalid params"}})
		}
		if hint, ok := hints[p.Event.Payload.Code]; ok && p.Event.CorrelationID != "" {
			// The host adds this plugin's ID. The operation ID is the event's
			// correlation ID, and cause is the event the note answers.
			err := request("event.publish", map[string]any{
				"type":    "plugin.annotation.added",
				"cause":   p.Event.ID,
				"payload": map[string]string{"operation_id": p.Event.CorrelationID, "text": hint, "level": "info"},
			})
			if err != nil {
				return err
			}
		}
		return answer(struct{}{})
	case "ping", "shutdown":
		return answer(struct{}{}) // the host closes standard input after shutdown
	case "settings.changed":
		return nil // a notification: this plugin has no settings
	}
	if len(m.ID) == 0 {
		return nil // an unknown notification
	}
	return enc.Encode(message{JSONRPC: "2.0", ID: m.ID, Error: &rpcError{Code: -32601, Message: "method not found"}})
}
