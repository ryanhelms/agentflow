package settings

import (
	"errors"
	"slices"
	"sort"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/harness"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
)

// CodeHarness: a harness override that harnessdef.Validate refuses (ADR
// "Phase 6 configuration keys"). A refused launcher or an unsafe argument is
// an error in the Save review.
const CodeHarness = "harness"

// overridesKeys is the path of personal:ai.harnesses.overrides.
var overridesKeys = []string{"ai", "harnesses", "overrides"}

// harnessLink checks every harness override an edit touches, in the
// personal file as it is after that edit: harnessdef.Validate for the
// personal layer, and status_from base only for an adapter (the kind may
// come from agentflow's overlay).
func harnessLink(c *check) {
	var overlay map[string]harnessdef.Definition
	for _, e := range c.live() {
		if e.layer != config.FieldPersonal || len(e.keys) < len(overridesKeys) || !slices.Equal(e.keys[:len(overridesKeys)], overridesKeys) {
			continue
		}
		if e.next == ([2]string{}) {
			continue
		}
		u, err := parseUser(e.next[0])
		if err != nil {
			continue // filesLink reports a file that does not load
		}
		ids := make([]string, 0, len(u.AI.Harnesses.Overrides))
		if len(e.keys) > len(overridesKeys) {
			if _, ok := u.AI.Harnesses.Overrides[e.keys[len(overridesKeys)]]; ok {
				ids = append(ids, e.keys[len(overridesKeys)])
			}
		} else {
			for id := range u.AI.Harnesses.Overrides {
				ids = append(ids, id)
			}
			sort.Strings(ids)
		}
		for _, id := range ids {
			d := u.AI.Harnesses.Overrides[id]
			if err := harnessdef.Validate(id, d, harnessdef.LayerPersonal); err != nil {
				msg := err.Error()
				var ve *harnessdef.ValidationError
				if errors.As(err, &ve) {
					msg = "Harness " + id + ": " + ve.Message + "."
					if ve.Field != "" {
						msg = "Harness " + id + ", " + ve.Field + ": " + ve.Message + "."
					}
				}
				c.add(SeverityError, e, CodeHarness, msg)
				break
			}
			if d.StatusFrom == harnessdef.StatusFromBase {
				if overlay == nil {
					overlay, _ = harness.LoadOverlay()
				}
				kind := d.Kind
				if kind == "" {
					kind = overlay[id].Kind
				}
				if kind != harnessdef.KindAdapter {
					c.add(SeverityError, e, CodeHarness, "Harness "+id+", status_from: base is only for a harness of kind adapter.")
					break
				}
			}
		}
	}
}
