package contract

import (
	"strings"
	"testing"
)

var roles = []string{
	"code_hosting", "task_management", "knowledge_base", "devops.checks",
	"devops.deployments", "devops.releases", "secops", "notifications",
}

func TestEveryRoleHasAContract(t *testing.T) {
	for _, role := range roles {
		c, ok := Current(role)
		if !ok || len(c.Operations) == 0 {
			t.Errorf("%s has no current contract", role)
		}
		for _, op := range c.Operations {
			switch op.Effects {
			case "read", "local", "remote":
			default:
				t.Errorf("%s %s has effects %q", c.ID(), op.Name, op.Effects)
			}
		}
	}
}

func TestResolve(t *testing.T) {
	if c, err := Resolve("task_management", ""); err != nil || c.ID() != "tasks/v1" {
		t.Fatalf("default = %v, %v", c.ID(), err)
	}
	if c, err := Resolve("task_management", "tasks/v1"); err != nil || c.Role != "task_management" {
		t.Fatalf("tasks/v1 = %+v, %v", c, err)
	}
	for id, want := range map[string]string{
		"tasks":        "expected NAME/vN",
		"tasks/v0":     "expected NAME/vN",
		"tasks/v9":     "unknown contract",
		"knowledge/v1": "does not apply",
	} {
		if _, err := Resolve("task_management", id); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Resolve(%q) = %v, want %q", id, err, want)
		}
	}
	if _, err := Resolve("nope", ""); err == nil {
		t.Error("unknown role resolved")
	}
}
