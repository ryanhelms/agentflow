package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/nscaledev/agentflow/internal/app"
)

func TestNotifyCLIPlansAndReportsConfigurationFailure(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("AGENTFLOW_HOME", filepath.Join(dir, "home"))
	t.Setenv("WEBHOOK_URL", "")
	if b, err := exec.Command("git", "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, b)
	}
	if err := os.Mkdir(".agentflow", 0o700); err != nil {
		t.Fatal(err)
	}
	const cfg = "notifications: {provider: webhook, settings: {destination: audit}}\n"
	if err := os.WriteFile(".agentflow/config.yml", []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	invoke := func(wantCode int, options ...string) app.NotificationResult {
		t.Helper()
		output, err := os.CreateTemp(t.TempDir(), "output")
		if err != nil {
			t.Fatal(err)
		}
		defer output.Close()
		previous := os.Stdout
		os.Stdout = output
		defer func() { os.Stdout = previous }()
		args := []string{"notify", "delivery", "--operation-id", "run/53", "--body", "failed"}
		if code := Main(append(args, options...)); code != wantCode {
			t.Fatalf("notify exit = %d, want %d", code, wantCode)
		}
		data, err := os.ReadFile(output.Name())
		if err != nil {
			t.Fatal(err)
		}
		var result app.NotificationResult
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatalf("invalid notification result %q: %v", data, err)
		}
		return result
	}
	preview := invoke(0, "--dry-run")
	if !preview.DryRun || len(preview.Deliveries) != 1 || preview.Deliveries[0].Status != app.NotificationPlanned {
		t.Fatalf("preview = %+v", preview)
	}
	failed := invoke(1)
	if len(failed.Deliveries) != 1 || failed.Deliveries[0].Destination != "audit" || failed.Deliveries[0].Status != app.NotificationFailed {
		t.Fatalf("failure = %+v", failed)
	}
	t.Setenv("WEBHOOK_URL", "https://example.invalid/webhook")
	if err := os.WriteFile(filepath.Join(os.Getenv("AGENTFLOW_HOME"), "config.yml"), []byte("autonomy: {remote: suggest}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	blocked := invoke(1, "--yes")
	if blocked.Reason == "" || len(blocked.Deliveries) != 1 || blocked.Deliveries[0].Status != app.NotificationPlanned {
		t.Fatalf("blocked = %+v", blocked)
	}
}
