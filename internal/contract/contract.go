// Package contract names the versioned operation sets that provider roles
// implement. Provider definitions declare a contract per role; compiled
// adapters implement the current version through internal/provider.
package contract

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Operation is one role operation. Optional operations are capabilities a
// provider may leave out; devflow reports them as unsupported.
type Operation struct {
	Name     string `json:"name"`
	Effects  string `json:"effects"` // read, local or remote
	Optional bool   `json:"optional,omitempty"`
}

// Contract is one major version of a role's operation set. Adding an
// operation or field is a minor change and keeps the version.
type Contract struct {
	Name       string      `json:"name"`
	Version    int         `json:"version"`
	Role       string      `json:"role"`
	Operations []Operation `json:"operations"`
}

func (c Contract) ID() string { return c.Name + "/v" + strconv.Itoa(c.Version) }

// Supported is how many major versions devflow accepts per contract: the
// current one and the one before it.
const Supported = 2

func read(name string) Operation   { return Operation{Name: name, Effects: "read"} }
func remote(name string) Operation { return Operation{Name: name, Effects: "remote"} }

var registry = []Contract{
	{Name: "tasks", Version: 1, Role: "task_management", Operations: []Operation{
		read("task.get"), read("task.find"), remote("task.create"), remote("task.transition"),
		{Name: "task.comment", Effects: "remote", Optional: true},
		{Name: "task.assign", Effects: "remote", Optional: true},
	}},
	{Name: "knowledge", Version: 1, Role: "knowledge_base", Operations: []Operation{
		read("docs.get"), read("docs.find"), remote("docs.publish"),
	}},
	{Name: "code-hosting", Version: 1, Role: "code_hosting", Operations: []Operation{
		read("change.find"), remote("change.open"), read("change.reviews"),
	}},
	{Name: "checks", Version: 1, Role: "devops.checks", Operations: []Operation{read("checks.list")}},
	{Name: "deployments", Version: 1, Role: "devops.deployments", Operations: []Operation{read("deployments.list")}},
	{Name: "releases", Version: 1, Role: "devops.releases", Operations: []Operation{
		read("release.get"), remote("release.publish"),
	}},
	{Name: "secops", Version: 1, Role: "secops", Operations: []Operation{
		read("findings.list"), {Name: "incidents.list", Effects: "read", Optional: true},
	}},
	{Name: "notifications", Version: 1, Role: "notifications", Operations: []Operation{remote("notify.send")}},
}

// All returns every registered contract version.
func All() []Contract { return slices.Clone(registry) }

// Current returns the newest contract for a role.
func Current(role string) (Contract, bool) {
	var best Contract
	found := false
	for _, c := range registry {
		if c.Role == role && (!found || c.Version > best.Version) {
			best, found = c, true
		}
	}
	return best, found
}

// Resolve checks that id ("tasks/v1") names a supported contract for role.
// An empty id selects the current contract.
func Resolve(role, id string) (Contract, error) {
	current, ok := Current(role)
	if !ok {
		return Contract{}, fmt.Errorf("no contract for role %s", role)
	}
	if id == "" {
		return current, nil
	}
	name, version, ok := strings.Cut(id, "/v")
	n, err := strconv.Atoi(version)
	if !ok || err != nil || n < 1 {
		return Contract{}, fmt.Errorf("contract %q: expected NAME/vN, for example %s", id, current.ID())
	}
	if name != current.Name {
		return Contract{}, fmt.Errorf("contract %q does not apply to %s; expected %s", id, role, current.ID())
	}
	for _, c := range registry {
		if c.Role == role && c.Version == n {
			if current.Version-n >= Supported {
				return Contract{}, fmt.Errorf("contract %s is no longer supported; use %s", id, current.ID())
			}
			return c, nil
		}
	}
	return Contract{}, fmt.Errorf("unknown contract %s; the current version is %s", id, current.ID())
}
