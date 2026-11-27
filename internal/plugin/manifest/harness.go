package manifest

import (
	"errors"
	"fmt"

	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
)

// HarnessEntry is one ACP harness definition a declarative plugin adds
// (extensions.harness, ADR "Phase 6 harness definitions"): an ID and the
// definition's fields inline. A plugin adds new IDs only, with bare command
// names and no sign-in check fields; harness.Compose refuses an ID that the
// registry, agentflow or another plugin already uses (id_taken).
type HarnessEntry struct {
	ID                    string `yaml:"id" json:"id"`
	harnessdef.Definition `yaml:",inline"`
}

// MaxHarnesses is the most harness entries one plugin may add.
const MaxHarnesses = 16

// harnesses checks extensions.harness: declarative plugins only, at most
// MaxHarnesses, unique IDs, each passing harnessdef.Validate for the plugin
// layer.
func (v *validator) harnesses(m Manifest) {
	e := m.Extensions.Harness
	if len(e) == 0 {
		return
	}
	if m.Tier != TierDeclarative {
		v.add("extensions.harness", CodeRule, "only a declarative plugin may add harnesses")
		return
	}
	if len(e) > MaxHarnesses {
		v.add("extensions.harness", CodeLimit, "a plugin adds at most %d harnesses", MaxHarnesses)
	}
	seen := map[string]bool{}
	for i, h := range e {
		at := fmt.Sprintf("extensions.harness[%d]", i)
		if seen[h.ID] {
			v.add(at+".id", CodeID, "two harnesses have the ID %q", h.ID)
			continue
		}
		seen[h.ID] = true
		if err := harnessdef.Validate(h.ID, h.Definition, harnessdef.LayerPlugin); err != nil {
			var ve *harnessdef.ValidationError
			if errors.As(err, &ve) {
				path := at
				if ve.Field != "" {
					path += "." + ve.Field
				} else {
					path += ".id"
				}
				v.add(path, ve.Code, "%s", ve.Message)
				continue
			}
			v.add(at, CodeRule, "%s", err.Error())
		}
	}
}
