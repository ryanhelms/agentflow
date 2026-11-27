package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// frames runs the plugin against the host's lines and returns what it wrote.
func frames(t *testing.T, input string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := serve(strings.NewReader(input), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var got []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("output line %q: %v", line, err)
		}
		got = append(got, m)
	}
	return got
}

const failedEvent = `{"jsonrpc":"2.0","id":"d1","method":"event.deliver","params":{"event":{"id":"01900000-0000-7000-8000-000000000001","type":"op.failed","correlation_id":"op_00112233445566778899aabbccddeeff","payload":{"operation":"commit","code":"%s"}}}}` + "\n"

func TestConversationFollowsTheProtocol(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocol_version":1}}` + "\n" +
		strings.Replace(failedEvent, "%s", "operation_changed", 1) +
		`{"jsonrpc":"2.0","id":"p1","method":"ping","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","method":"settings.changed","params":{"settings":{}}}` + "\n" +
		`{"jsonrpc":"2.0","id":"s1","method":"shutdown","params":{}}` + "\n"
	got := frames(t, input)
	if len(got) != 6 {
		t.Fatalf("%d frames: %v", len(got), got)
	}
	// initialize is answered first, with the same protocol version.
	if got[0]["id"] != float64(1) || got[0]["result"].(map[string]any)["protocol_version"] != float64(1) {
		t.Errorf("initialize answer %v", got[0])
	}
	// Then the plugin chooses what it hears about.
	if got[1]["method"] != "subscribe" || got[1]["params"].(map[string]any)["types"].([]any)[0] != "op.failed" {
		t.Errorf("subscribe %v", got[1])
	}
	// A failed operation gets a note on that operation, caused by the event.
	pub := got[2]
	params, _ := pub["params"].(map[string]any)
	payload, _ := params["payload"].(map[string]any)
	if pub["method"] != "event.publish" || params["type"] != "plugin.annotation.added" || params["cause"] != "01900000-0000-7000-8000-000000000001" ||
		payload["operation_id"] != "op_00112233445566778899aabbccddeeff" || payload["text"] != hints["operation_changed"] || payload["level"] != "info" {
		t.Errorf("publish %v", pub)
	}
	if got[2]["id"] == got[1]["id"] {
		t.Error("two requests share an ID")
	}
	// The delivery is answered after the note is sent, with the same ID.
	if got[3]["id"] != "d1" || got[3]["error"] != nil || got[3]["result"] == nil {
		t.Errorf("deliver answer %v", got[3])
	}
	if got[4]["id"] != "p1" || got[5]["id"] != "s1" {
		t.Errorf("ping and shutdown answers %v %v", got[4], got[5])
	}
	for _, f := range got {
		if f["jsonrpc"] != "2.0" {
			t.Errorf("frame without jsonrpc 2.0: %v", f)
		}
	}
}

func TestACodeWithoutAHintGetsNoNote(t *testing.T) {
	got := frames(t, strings.Replace(failedEvent, "%s", "error", 1))
	if len(got) != 1 || got[0]["id"] != "d1" || got[0]["result"] == nil {
		t.Fatalf("frames %v", got)
	}
}

func TestEveryHintIsOneShortLine(t *testing.T) {
	for code, hint := range hints {
		if hint == "" || strings.ContainsAny(hint, "\n\r") || len(hint) > 200 {
			t.Errorf("hint for %s: %q", code, hint)
		}
	}
}

func TestUnknownRequestsAreRefusedAndNotificationsIgnored(t *testing.T) {
	got := frames(t, `{"jsonrpc":"2.0","id":7,"method":"nope","params":{}}`+"\n"+`{"jsonrpc":"2.0","method":"nope2"}`+"\n")
	if len(got) != 1 || got[0]["id"] != float64(7) || got[0]["error"].(map[string]any)["code"] != float64(-32601) {
		t.Fatalf("frames %v", got)
	}
}

func TestAFrameThatIsNotJSONEndsTheProgram(t *testing.T) {
	var out bytes.Buffer
	if err := serve(strings.NewReader("not json\n"), &out); err == nil {
		t.Fatal("serve accepted a line that is not JSON")
	}
}

func TestALastLineWithoutANewlineIsStillHandled(t *testing.T) {
	got := frames(t, `{"jsonrpc":"2.0","id":"p","method":"ping"}`)
	if len(got) != 1 || got[0]["id"] != "p" {
		t.Fatalf("frames %v", got)
	}
}
