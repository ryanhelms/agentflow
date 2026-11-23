package initproviders

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/consolecfg"
	"github.com/nscaledev/devflow/templates"
	"gopkg.in/yaml.v3"
)

// Edit contains the config expected immediately after scaffolding and the validated replacement.
type Edit struct{ Before, After []byte }

// Prepare validates the explicit answers without writing any files. A missing repository
// config starts from the same template that init will install.
func (p *Plan) Prepare(answers map[string]string, force bool) (*Edit, error) {
	if err := p.ValidateAnswers(answers); err != nil {
		return nil, err
	}
	if len(answers) == 0 {
		return nil, nil
	}
	base := p.original
	if base == nil || force {
		var err error
		base, err = templates.Repo.ReadFile("repo/" + configFile)
		if err != nil {
			return nil, err
		}
	}
	var fields map[string]any
	if err := yaml.Unmarshal(base, &fields); err != nil {
		return nil, err
	}
	next := string(base)
	if len(bytes.TrimSpace(base)) == 0 {
		next = "devflow: 1\n"
	}
	for _, role := range p.Roles {
		id, ok := answers[role.Role]
		if !ok || property(fields, role.Role+".provider") == id {
			continue
		}
		v, err := json.Marshal(id)
		if err != nil {
			return nil, err
		}
		next, err = consolecfg.EditSetting(next, role.Role, "", v, false)
		if err != nil {
			return nil, fmt.Errorf("set %s: %w", role.Role, err)
		}
	}
	if bytes.Equal(base, []byte(next)) {
		return nil, nil
	}
	_, _, err := config.InspectRepo([]byte(next), p.definitions)
	if err != nil {
		return nil, err
	}
	return &Edit{Before: base, After: []byte(next)}, nil
}

// ValidateAnswers checks script-supplied choices before the initializer asks any questions.
func (p *Plan) ValidateAnswers(answers map[string]string) error {
	for role, id := range answers {
		if !slices.ContainsFunc(p.Roles, func(r Role) bool { return role == r.Role && slices.Contains(r.Options, id) }) {
			return fmt.Errorf("invalid provider selection %q for role %q", id, role)
		}
	}
	return nil
}
