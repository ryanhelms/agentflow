package provision

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

type Action string

const (
	Verify  Action = "verify"
	Install Action = "install"
)

// Step describes one planned action for all providers sharing a target. Artifact
// is set on installs and on verification of an exact artifact.
type Step struct {
	Target    Target
	Providers []string
	Versions  VersionRange
	Action    Action
	Artifact  *Artifact
}

// Plan is an environment-bound snapshot produced by BuildPlan.
type Plan struct {
	env   Environment
	steps []plannedStep
}

type plannedStep struct {
	Step
	before Installation
}

func (p Plan) Environment() Environment { return p.env }

// Steps returns a copy for inspection. Changing it does not change the plan.
func (p Plan) Steps() []Step {
	steps := make([]Step, 0, len(p.steps))
	for _, planned := range p.steps {
		step := planned.Step
		step.Target = cloneTarget(step.Target)
		step.Providers = slices.Clone(step.Providers)
		step.Versions.Before = copyPointer(step.Versions.Before)
		step.Artifact = copyPointer(step.Artifact)
		steps = append(steps, step)
	}
	return steps
}

func cloneTarget(target Target) Target {
	target.Args = slices.Clone(target.Args)
	return target
}

func copyPointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// ConflictError identifies incompatible or unresolvable declarations for one companion.
type ConflictError struct {
	CompanionID string
	Providers   []string
	Reason      string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("companion %q (%s): %s", e.CompanionID, strings.Join(e.Providers, ", "), e.Reason)
}

type declaration struct {
	provider string
	Companion
}

// BuildPlan probes the named environment without mutating it. Steps and conflicts
// are ordered by companion ID. A plan is returned only when all declarations can
// be reconciled or verified.
func BuildPlan(ctx context.Context, env Environment, manifests []Manifest, probe Prober) (Plan, error) {
	if !identifier.MatchString(env.ID) || !env.Platform.valid() || probe == nil {
		return Plan{}, errors.New("planning requires a valid environment, platform and prober")
	}
	groups, err := groupDeclarations(manifests)
	if err != nil {
		return Plan{}, err
	}
	ids := make([]string, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	plan := Plan{env: env}
	var conflicts []error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return Plan{}, err
		}
		step, err := planCompanion(ctx, env, probe, groups[id])
		if err != nil {
			var conflict *ConflictError
			if !errors.As(err, &conflict) {
				return Plan{}, err
			}
			conflicts = append(conflicts, err)
			continue
		}
		plan.steps = append(plan.steps, step)
	}
	if len(conflicts) != 0 {
		return Plan{}, errors.Join(conflicts...)
	}
	return plan, nil
}

func groupDeclarations(manifests []Manifest) (map[string][]declaration, error) {
	groups := make(map[string][]declaration)
	providers := make(map[string]bool)
	for _, manifest := range manifests {
		id := manifest.ProviderID
		if !identifier.MatchString(id) || providers[id] {
			return nil, fmt.Errorf("invalid or duplicate provider id %q", id)
		}
		providers[id] = true
		seen := make(map[string]bool)
		for _, companion := range manifest.Companions {
			targetID := companion.Target.ID
			if seen[targetID] {
				return nil, fmt.Errorf("provider %q declares companion %q more than once", id, targetID)
			}
			seen[targetID] = true
			if err := companion.validate(); err != nil {
				return nil, fmt.Errorf("provider %q companion %q: %w", id, targetID, err)
			}
			groups[targetID] = append(groups[targetID], declaration{id, companion})
		}
	}
	return groups, nil
}

func planCompanion(ctx context.Context, env Environment, probe Prober, declarations []declaration) (plannedStep, error) {
	slices.SortFunc(declarations, func(a, b declaration) int {
		return strings.Compare(a.provider, b.provider)
	})
	step := Step{Target: cloneTarget(declarations[0].Target), Action: Verify}
	for _, decl := range declarations {
		step.Providers = append(step.Providers, decl.provider)
	}
	conflict := func(reason string) (plannedStep, error) {
		return plannedStep{}, &ConflictError{step.Target.ID, step.Providers, reason}
	}
	var artifacts []Artifact
	for _, decl := range declarations {
		if !sameTarget(step.Target, decl.Target) {
			return conflict("target, auth reference or health probe differs")
		}
		if len(decl.Platforms) != 0 && !slices.Contains(decl.Platforms, env.Platform) {
			return conflict(fmt.Sprintf("provider %q does not support %s/%s", decl.provider, env.Platform.OS, env.Platform.Arch))
		}
		step.Versions = step.Versions.intersect(decl.Versions)
		available := artifactsFor(decl.Artifacts, env.Platform)
		artifacts = append(artifacts, available...)
		if decl.PinArtifact {
			if len(available) != 1 {
				return conflict(fmt.Sprintf("provider %q must pin one artifact for this platform", decl.provider))
			}
			if step.Artifact != nil && *step.Artifact != available[0] {
				return conflict("exact artifacts differ")
			}
			step.Artifact = &available[0]
		}
	}
	if !step.Versions.contains(step.Versions.Min) {
		return conflict("version ranges do not overlap")
	}
	step.Versions.Before = copyPointer(step.Versions.Before)
	if step.Artifact != nil && !step.Versions.contains(step.Artifact.Version) {
		return conflict("exact artifact is outside the shared version range")
	}
	observed, err := observe(ctx, probe, env, step.Target)
	if err != nil {
		return plannedStep{}, err
	}
	if step.Target.Kind != RemoteMCP && !matches(step, observed.Installation) {
		if step.Artifact == nil {
			artifact, reason := chooseArtifact(artifacts, step.Versions)
			if reason != "" {
				return conflict(reason)
			}
			step.Artifact = &artifact
		}
		step.Action = Install
	}
	return plannedStep{step, observed.Installation}, nil
}

func sameTarget(a, b Target) bool {
	if !slices.Equal(a.Args, b.Args) {
		return false
	}
	return a.Kind == b.Kind && a.Command == b.Command && a.Endpoint == b.Endpoint &&
		a.AuthRef == b.AuthRef && a.Health == b.Health
}

func artifactsFor(artifacts []Artifact, platform Platform) []Artifact {
	var available []Artifact
	for _, artifact := range artifacts {
		if artifact.Platform == platform && !slices.Contains(available, artifact) {
			available = append(available, artifact)
		}
	}
	return available
}

func chooseArtifact(artifacts []Artifact, versions VersionRange) (Artifact, string) {
	var chosen Artifact
	ambiguous := false
	for _, candidate := range artifacts {
		if !versions.contains(candidate.Version) {
			continue
		}
		switch order := compareVersion(candidate.Version, chosen.Version); {
		case chosen.Source == "" || order > 0:
			chosen = candidate
			ambiguous = false
		case order == 0 && candidate != chosen:
			ambiguous = true
		}
	}
	switch {
	case chosen.Source == "":
		return Artifact{}, "no declared artifact satisfies the shared version range for this platform"
	case ambiguous:
		return Artifact{}, "highest compatible version has different declared artifacts"
	default:
		return chosen, ""
	}
}
