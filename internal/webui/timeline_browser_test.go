package webui

import "testing"

// Drive the timeline panel from a scripted stream: rendering, live changes,
// filters, links, failure codes and the jump to the Approvals panel.
func TestTimelineBrowser(t *testing.T) {
	liveConsoleFixture(t, "testdata/timeline-test.js")
}
