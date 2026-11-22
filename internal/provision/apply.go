package provision

import (
	"context"
	"errors"
	"fmt"
)

var (
	ErrStalePlan    = errors.New("installation changed since planning")
	ErrVerification = errors.New("companion verification failed")
)

// Installation is the observable state an installer can compare before writing.
// SHA256 is the measured digest of the installed bytes, when available.
type Installation struct {
	Present      bool
	VersionKnown bool
	Version      Version
	SHA256       string
}

// Observation reports presence, version, artifact integrity, and probe outcomes.
// A remote target is present when its endpoint is discovered by the probe.
type Observation struct {
	Installation Installation
	Healthy      bool
	AuthReady    bool
}

// Prober makes read-only observations in the specified environment. Expected
// absences and failed health or auth checks are observations. An error means the
// probe could not determine state.
type Prober interface {
	Observe(context.Context, Environment, Target) (Observation, error)
}

// Installer publishes the exact declared artifact in the specified environment.
// Implementations must validate the source and digest before publication, commit
// atomically, and compare expected state at commit time to prevent lost updates.
type Installer interface {
	Install(ctx context.Context, env Environment, target Target, artifact Artifact, expected Installation) error
}

// Apply executes the plan in order, stopping at the first failure. Every step
// gets a fresh observation. Apply verifies each installation before advancing.
func Apply(ctx context.Context, env Environment, plan Plan, probe Prober, installer Installer) error {
	if env != plan.env || plan.env.ID == "" || probe == nil {
		return errors.New("apply requires a plan and prober for the same environment")
	}
	for _, step := range plan.steps {
		observed, err := observe(ctx, probe, env, step.Target)
		if err != nil {
			return err
		}
		if step.Action == Install && !matches(step.Step, observed.Installation) {
			if observed.Installation != step.before {
				return fmt.Errorf("companion %q: %w", step.Target.ID, ErrStalePlan)
			}
			if installer == nil {
				return fmt.Errorf("companion %q requires an installer", step.Target.ID)
			}
			if err := installer.Install(ctx, env, cloneTarget(step.Target), *step.Artifact, step.before); err != nil {
				return fmt.Errorf("install companion %q: %w", step.Target.ID, err)
			}
			observed, err = observe(ctx, probe, env, step.Target)
			if err != nil {
				return err
			}
		}
		if err := verify(step.Step, observed); err != nil {
			return fmt.Errorf("companion %q: %w", step.Target.ID, err)
		}
	}
	return ctx.Err()
}

func observe(ctx context.Context, probe Prober, env Environment, target Target) (Observation, error) {
	if target.Health.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, target.Health.Timeout)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	observed, err := probe.Observe(ctx, env, cloneTarget(target))
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return Observation{}, fmt.Errorf("probe companion %q: %w", target.ID, err)
	}
	if !observed.Installation.Present {
		observed.Installation = Installation{}
	}
	return observed, nil
}

func matches(step Step, installation Installation) bool {
	if !installation.Present {
		return false
	}
	if step.Artifact != nil {
		return installation.VersionKnown && installation.Version == step.Artifact.Version &&
			installation.SHA256 == step.Artifact.SHA256
	}
	if installation.VersionKnown {
		return step.Versions.contains(installation.Version)
	}
	return step.Versions.Min == (Version{}) && step.Versions.Before == nil
}

func verify(step Step, observed Observation) error {
	var reason string
	switch {
	case !observed.Installation.Present:
		reason = "companion is missing"
	case !matches(step, observed.Installation):
		reason = "observed version or artifact digest differs from the plan"
	case step.Target.AuthRef != "" && !observed.AuthReady:
		reason = "auth reference is not ready"
	case !observed.Healthy:
		reason = "health probe did not pass"
	default:
		return nil
	}
	return fmt.Errorf("%w: %s", ErrVerification, reason)
}
