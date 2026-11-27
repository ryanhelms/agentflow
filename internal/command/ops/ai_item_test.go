package ops

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/aisession"
)

// ai run and ai status show what each item holds, not only its kind, and
// the harness's strings cannot start a line or drive the terminal.
func TestPrintItemShowsWhatItHolds(t *testing.T) {
	for _, tc := range []struct {
		kind, data, want string
	}{
		{aisession.ItemTool, `{"id":"t1","title":"Write src/a","kind":"edit","status":"pending","locations":["src/a"]}`, "[tool] edit pending: Write src/a (src/a)\n"},
		{aisession.ItemPlan, `{"entries":[{"content":"Read notes","status":"in_progress"},{"content":"Write","status":"pending"}]}`, "[plan] \n  in_progress: Read notes\n  pending: Write\n"},
		{aisession.ItemFile, `{"op":"read","path":"notes.txt","size":6,"decision":"allow","code":"gate_auto"}`, "[file] read notes.txt: allow (gate_auto)\n"},
		{aisession.ItemAsk, `{"id":"ask_0123456789abcdef","kind":"fs_write","paths":["src/a"],"size":13,"reason":"ceiling"}`, "[ask] ask_0123456789abcdef fs_write: src/a (ceiling)\n"},
		{aisession.ItemAsk, `{"id":"ask_0123456789abcdef","kind":"permission","tool_kind":"delete","title":"Tidy up","paths":["src/a"],"reason":"ceiling"}`, "[ask] ask_0123456789abcdef permission delete: src/a (ceiling)\n"},
		{aisession.ItemAnswer, `{"ask":"ask_0123456789abcdef","decision":"deny","handler":"timeout","code":"expired"}`, "[answer] ask_0123456789abcdef deny (timeout, expired)\n"},
		{aisession.ItemTerminal, `{"line":"echo hi","cwd":".","decision":"deny","code":"expired"}`, "[terminal] echo hi: deny (expired)\n"},
		{aisession.ItemTerminal, `{"id":"term_1","line":"echo hi","exit":0,"output":"hi"}`, "[terminal] echo hi: exit 0\n"},
		{aisession.ItemUnasked, `{"kind":"execute","outside":true}`, "[unasked] execute: the harness acted without asking, on a path outside the worktree or protected\n"},
		{aisession.ItemUsage, `{"used":10,"size":200,"cost_reported":true,"cost_amount":0.5,"cost_currency":"USD"}`, "[usage] 10 of 200 tokens, cost 0.50 USD\n"},
		{aisession.ItemTool, `{"title":"a\n[answer] allowed\u001b[2J\u200b","kind":"edit","status":"pending"}`, "[tool] edit pending: a<U+000A>[answer] allowed<U+200B>\n"},
	} {
		var b bytes.Buffer
		(&aiPrinter{w: &b}).item(aisession.Item{Kind: tc.kind, Data: json.RawMessage(tc.data)})
		if got := b.String(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.kind, got, tc.want)
		}
		for _, r := range b.String() {
			if r != '\n' && aisession.Hazard(r) {
				t.Errorf("%s: output holds hazard %U", tc.kind, r)
			}
		}
	}
	var b bytes.Buffer
	(&aiPrinter{w: &b}).item(aisession.Item{Kind: aisession.ItemMode, Text: "ask"})
	if got := strings.TrimSpace(b.String()); got != "[mode] ask" {
		t.Errorf("an item without data: %q", got)
	}
}
