package cli

import (
	"os"
	"strings"
	"testing"
)

func TestConsoleClearsCheckoutSpecificGitEnvironment(t *testing.T) {
	for _, entry := range os.Environ() {
		key, val, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") {
			t.Setenv(key, val)
		}
	}
	const value = "caller-setting"
	checkout := []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0"}
	for _, key := range checkout {
		t.Setenv(key, value)
	}
	t.Setenv("GIT_AUTHOR_NAME", value)
	clearWorktreeGitEnv()
	for _, key := range checkout {
		if _, exists := os.LookupEnv(key); exists {
			t.Errorf("%s still overrides daemon checkout selection", key)
		}
	}
	if got := os.Getenv("GIT_AUTHOR_NAME"); got != value {
		t.Errorf("git author = %q, want %q", got, value)
	}
}
