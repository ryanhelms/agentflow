package configconsole

import (
	"strings"
	"testing"

	"github.com/nscaledev/agentflow/internal/setup"
)

func TestConnectionDetailsOmitPrivateProviderData(t *testing.T) {
	const sensitivePath = "secret-stdout-example"
	for _, provider := range []string{"github", "jira", "filesystem", "asana", "custom"} {
		detail := connectionDetail(setup.ProviderRequirement{Role: "task_management", Provider: provider, Detail: "check connectivity and run gh auth login " + sensitivePath})
		if strings.Contains(detail, sensitivePath) || detail == "" {
			t.Fatal("unsafe provider output")
		}
	}
}
