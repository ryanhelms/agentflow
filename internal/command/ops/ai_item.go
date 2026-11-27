package ops

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ryanhelms/agentflow/internal/aisession"
)

// itemView is the union of what the transcript's item kinds hold in Data.
type itemView struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Title     string   `json:"title"`
	Status    string   `json:"status"`
	Locations []string `json:"locations"`
	Unasked   bool     `json:"unasked"`
	Outside   bool     `json:"outside"`
	ToolKind  string   `json:"tool_kind"`
	Entries   []struct {
		Content string `json:"content"`
		Status  string `json:"status"`
	} `json:"entries"`
	Op           string   `json:"op"`
	Path         string   `json:"path"`
	Decision     string   `json:"decision"`
	Code         string   `json:"code"`
	Handler      string   `json:"handler"`
	Ask          string   `json:"ask"`
	Paths        []string `json:"paths"`
	Line         string   `json:"line"`
	Reason       string   `json:"reason"`
	Exit         *int     `json:"exit"`
	Signal       string   `json:"signal"`
	Used         uint64   `json:"used"`
	Size         uint64   `json:"size"`
	CostReported bool     `json:"cost_reported"`
	CostAmount   float64  `json:"cost_amount"`
	CostCurrency string   `json:"cost_currency"`
}

// itemDetail says what an item other than a message holds: one line, or for
// a plan one line per entry. Every string comes from the harness or names
// what it touched, so each goes through visible, which also keeps it from
// starting a line of its own. An item without Data keeps its Text.
func itemDetail(it aisession.Item) string {
	var v itemView
	if len(it.Data) == 0 || json.Unmarshal(it.Data, &v) != nil {
		return strings.ReplaceAll(aiText(it.Text), "\n", " ")
	}
	switch it.Kind {
	case aisession.ItemTool:
		s := visible(strings.TrimSpace(v.Kind+" "+v.Status)) + ": " + visible(v.Title)
		if len(v.Locations) > 0 {
			s += " (" + visible(strings.Join(v.Locations, " ")) + ")"
		}
		if v.Unasked {
			s += ", without asking"
		}
		return s
	case aisession.ItemPlan:
		var b strings.Builder
		for _, e := range v.Entries {
			fmt.Fprintf(&b, "\n  %s: %s", visible(e.Status), visible(e.Content))
		}
		return b.String()
	case aisession.ItemFile:
		return fmt.Sprintf("%s %s: %s (%s)", visible(v.Op), visible(v.Path), visible(v.Decision), visible(v.Code))
	case aisession.ItemAsk:
		kind := strings.TrimSpace(v.Kind + " " + v.ToolKind)
		return fmt.Sprintf("%s %s: %s (%s)", visible(v.ID), visible(kind), visible(askSubject(v.Line, v.Paths, v.Title)), visible(v.Reason))
	case aisession.ItemAnswer:
		return fmt.Sprintf("%s %s (%s, %s)", visible(v.Ask), visible(v.Decision), visible(v.Handler), visible(v.Code))
	case aisession.ItemTerminal:
		s := visible(v.Line)
		switch {
		case v.Decision != "":
			s += fmt.Sprintf(": %s (%s)", visible(v.Decision), visible(v.Code))
		case v.Exit != nil:
			s += fmt.Sprintf(": exit %d", *v.Exit)
		case v.Signal != "":
			s += ": signal " + visible(v.Signal)
		}
		return s
	case aisession.ItemUnasked:
		s := visible(v.Kind) + ": the harness acted without asking"
		if v.Outside {
			s += ", on a path outside the worktree or protected"
		}
		return s
	case aisession.ItemUsage:
		s := fmt.Sprintf("%d of %d tokens", v.Used, v.Size)
		if v.CostReported {
			s += fmt.Sprintf(", cost %.2f %s", v.CostAmount, visible(v.CostCurrency))
		}
		return s
	}
	return strings.ReplaceAll(aiText(it.Text), "\n", " ")
}
