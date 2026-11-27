package webui

import (
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/gitx"
)

func aiServer(dir string) *server {
	return &server{repo: &gitx.Repo{Dir: dir}, origin: "http://127.0.0.1:41511", instanceKey: randomKey(), session: randomKey(), rememberKey: randomKey()}
}

// The routes sit behind the console's origin, request-header and session
// checks, and answer 503 in a build without the ai commands. The rest of
// their behaviour is tested with the real Specs in ai_console_test.go.
func TestAIRoutes(t *testing.T) {
	s := testServer(t)
	for _, path := range []string{"/api/ai/respond", "/api/ai/cancel"} {
		if w := harnessRequest(t, s, path, "{}", map[string]string{"Origin": "http://evil.invalid"}); w.Code != 403 {
			t.Errorf("%s from another origin: %d", path, w.Code)
		}
		if w := harnessRequest(t, s, path, "{}", map[string]string{"X-Agentflow-Request": ""}); w.Code != 403 {
			t.Errorf("%s without the request header: %d", path, w.Code)
		}
		if w := harnessRequest(t, s, path, "{}", map[string]string{"Authorization": ""}); w.Code != 401 {
			t.Errorf("%s without a session: %d", path, w.Code)
		}
	}
	old := aiCommands
	empty := command.NewRegistry()
	aiCommands = func() *command.Registry { return empty }
	t.Cleanup(func() { aiCommands = old })
	sess := string(aisession.NewID())
	good := `{"session":"` + sess + `","ask":"ask_0123456789abcdef","decision":"allow","fingerprint":"` + strings.Repeat("ab", 32) + `"}`
	if w := harnessRequest(t, s, "/api/ai/respond", good, nil); w.Code != 503 {
		t.Errorf("without the commands: %d", w.Code)
	}
}
