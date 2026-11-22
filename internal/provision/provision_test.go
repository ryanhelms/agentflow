package provision

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

var testEnv = Environment{ID: "dev", Platform: Platform{OS: "linux", Arch: "arm64"}}

type fakeEnvironment struct {
	state     map[string]Observation
	probes    []string
	installs  []string
	failAfter string
	badDigest bool
}

func (f *fakeEnvironment) Observe(_ context.Context, env Environment, target Target) (Observation, error) {
	if env != testEnv {
		return Observation{}, errors.New("unexpected probe environment")
	}
	f.probes = append(f.probes, target.ID)
	return f.state[target.ID], nil
}

func (f *fakeEnvironment) Install(_ context.Context, env Environment, target Target, artifact Artifact, expected Installation) error {
	if env != testEnv || artifact.Platform != env.Platform || f.state[target.ID].Installation != expected {
		return errors.New("unexpected install scope or precondition")
	}
	f.installs = append(f.installs, target.ID)
	current := installed(artifact)
	if f.badDigest {
		current.Installation.SHA256 = ""
	}
	f.state[target.ID] = current
	if f.failAfter == target.ID {
		return errors.New("interrupted after publication")
	}
	return nil
}

func installed(artifact Artifact) Observation {
	return Observation{
		Installation: Installation{Present: true, VersionKnown: true, Version: artifact.Version, SHA256: artifact.SHA256},
		Healthy:      true,
		AuthReady:    true,
	}
}

func artifact(major uint) Artifact {
	return Artifact{
		Platform: testEnv.Platform, Version: Version{Major: major},
		Source: "https://downloads.example/companion", SHA256: strings.Repeat("a", 64),
	}
}

func cli(id string, artifacts ...Artifact) Companion {
	return Companion{
		Target:    Target{ID: id, Kind: CLI, Command: id, Health: HealthProbe{Name: "version"}},
		Artifacts: artifacts,
	}
}

func build(t *testing.T, fake *fakeEnvironment, manifests ...Manifest) Plan {
	t.Helper()
	plan, err := BuildPlan(t.Context(), testEnv, manifests, fake)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestPlanCoalescesAndOrdersCompanions(t *testing.T) {
	one, two, three := artifact(1), artifact(2), artifact(3)
	upper := three.Version
	a := cli("shared", one, three)
	a.Versions.Before = &upper
	b := cli("shared", two)
	b.Versions.Min = two.Version
	a.Target.Kind, b.Target.Kind = StdioMCP, StdioMCP
	a.Target.Args, b.Target.Args = []string{"serve"}, []string{"serve"}
	p := Manifest{"provider-a", []Companion{a, cli("first", one)}}
	q := Manifest{"provider-b", []Companion{b}}
	fake := &fakeEnvironment{state: make(map[string]Observation)}
	plan := build(t, fake, q, p)
	steps := plan.Steps()
	if len(steps) != 2 || steps[0].Target.ID != "first" || steps[1].Target.ID != "shared" ||
		!slices.Equal(steps[1].Providers, []string{"provider-a", "provider-b"}) ||
		steps[1].Action != Install || steps[1].Artifact == nil || *steps[1].Artifact != two ||
		!slices.Equal(fake.probes, []string{"first", "shared"}) || len(fake.installs) != 0 {
		t.Fatalf("unexpected plan: %+v, probes: %v, installs: %v", steps, fake.probes, fake.installs)
	}
	slices.Reverse(p.Companions)
	if reordered := build(t, fake, p, q); !reflect.DeepEqual(plan.Steps(), reordered.Steps()) {
		t.Fatalf("plan depends on declaration order: %+v", reordered.Steps())
	}
	// Changing the input and preview cannot alter the approved actions.
	a.Target.Args[0] = "changed"
	upper = one.Version
	*steps[1].Artifact = three
	steps[1].Target.Args[0] = "changed again"
	if fresh := plan.Steps()[1]; fresh.Target.Args[0] != "serve" || *fresh.Artifact != two ||
		*fresh.Versions.Before != three.Version {
		t.Fatalf("plan was changed: %+v", fresh)
	}
	if err := Apply(t.Context(), testEnv, plan, fake, fake); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fake.installs, []string{"first", "shared"}) {
		t.Fatalf("installs = %v", fake.installs)
	}
}

func TestPlanConflicts(t *testing.T) {
	one, two := artifact(1), artifact(2)
	for _, tc := range []struct {
		name, reason string
		change       func(a, b *Companion)
	}{
		{"launch", "target", func(_, b *Companion) { b.Target.Command = "other" }},
		{"platform", "does not support", func(_, b *Companion) {
			b.Artifacts = nil
			b.Platforms = []Platform{{OS: "darwin", Arch: "arm64"}}
		}},
		{"range", "do not overlap", func(a, b *Companion) { a.Versions.Before = &two.Version; b.Versions.Min = two.Version }},
		{"pin", "exact artifacts differ", func(a, b *Companion) {
			a.PinArtifact, b.PinArtifact = true, true
			b.Artifacts = []Artifact{two}
		}},
		{"pin outside range", "outside the shared version range", func(a, b *Companion) {
			a.PinArtifact = true
			b.Versions.Min = two.Version
		}},
		{"no compatible artifact", "no declared artifact", func(_, b *Companion) { b.Versions.Min = two.Version }},
		{"ambiguous artifact", "different declared artifacts", func(_, b *Companion) {
			b.Artifacts[0].SHA256 = strings.Repeat("b", 64)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := cli("shared", one), cli("shared", one)
			tc.change(&a, &b)
			plan, err := BuildPlan(t.Context(), testEnv, []Manifest{{"b", []Companion{b}}, {"a", []Companion{a}}}, &fakeEnvironment{})
			var conflict *ConflictError
			if !errors.As(err, &conflict) || conflict.CompanionID != "shared" ||
				!slices.Equal(conflict.Providers, []string{"a", "b"}) ||
				!strings.Contains(conflict.Reason, tc.reason) || len(plan.Steps()) != 0 {
				t.Fatalf("expected shared companion conflict, got plan %+v and error %v", plan.Steps(), err)
			}
		})
	}
}

func TestApplyReplansAfterInterruptedInstall(t *testing.T) {
	one := artifact(1)
	a, b := cli("a", one), cli("b", one)
	a.PinArtifact = true
	manifest := Manifest{"provider", []Companion{b, a}}
	fake := &fakeEnvironment{state: make(map[string]Observation), failAfter: "a"}
	plan := build(t, fake, manifest)
	if err := Apply(t.Context(), testEnv, plan, fake, fake); err == nil ||
		!slices.Equal(fake.installs, []string{"a"}) {
		t.Fatalf("interrupted apply: error %v, installs %v", err, fake.installs)
	}
	replanned := build(t, fake, manifest)
	steps := replanned.Steps()
	if steps[0].Action != Verify || steps[0].Artifact == nil || steps[1].Action != Install {
		t.Fatalf("replan = %+v", steps)
	}
	if err := Apply(t.Context(), testEnv, replanned, fake, fake); err != nil {
		t.Fatal(err)
	}
	if err := Apply(t.Context(), testEnv, plan, fake, nil); err != nil {
		t.Fatalf("replaying satisfied plan: %v", err)
	}
	if !slices.Equal(fake.installs, []string{"a", "b"}) {
		t.Fatalf("installs = %v", fake.installs)
	}
}

func TestApplyChecksEnvironmentDriftAndInstalledDigest(t *testing.T) {
	one := artifact(1)
	pinned := cli("a", one)
	pinned.PinArtifact = true
	fake := &fakeEnvironment{state: make(map[string]Observation)}
	plan := build(t, fake, Manifest{"provider", []Companion{pinned}})
	probes := len(fake.probes)
	other := testEnv
	other.ID = "other"
	if err := Apply(t.Context(), other, plan, fake, fake); err == nil || len(fake.probes) != probes {
		t.Fatalf("wrong environment applied: error %v, probes %v", err, fake.probes)
	}
	fake.state["a"] = installed(artifact(2))
	if err := Apply(t.Context(), testEnv, plan, fake, fake); !errors.Is(err, ErrStalePlan) || len(fake.installs) != 0 {
		t.Fatalf("stale apply: error %v, installs %v", err, fake.installs)
	}
	fake.badDigest = true
	replanned := build(t, fake, Manifest{"provider", []Companion{pinned}})
	if err := Apply(t.Context(), testEnv, replanned, fake, fake); !errors.Is(err, ErrVerification) {
		t.Fatalf("expected digest verification failure, got %v", err)
	}
	if action := build(t, fake, Manifest{"provider", []Companion{pinned}}).Steps()[0].Action; action != Install {
		t.Fatalf("expected corrupt artifact to be replanned, got %s", action)
	}
}

func TestVerificationOnly(t *testing.T) {
	local := cli("local", artifact(1))
	remote := Companion{Target: Target{
		ID: "remote", Kind: RemoteMCP, Endpoint: "https://mcp.example/api",
		AuthRef: "provider/login", Health: HealthProbe{Name: "initialize"},
	}}
	fake := &fakeEnvironment{state: map[string]Observation{
		"local":  installed(artifact(2)),
		"remote": {Installation: Installation{Present: true}, Healthy: true},
	}}
	plan := build(t, fake, Manifest{"provider", []Companion{remote, local}})
	for _, step := range plan.Steps() {
		if step.Action != Verify || step.Artifact != nil {
			t.Fatalf("unexpected install: %+v", step)
		}
	}
	if err := Apply(t.Context(), testEnv, plan, fake, fake); !errors.Is(err, ErrVerification) ||
		!strings.Contains(err.Error(), "auth reference") {
		t.Fatalf("expected remote auth failure, got %v", err)
	}
	fake.state["remote"] = Observation{Installation: Installation{Present: true}, AuthReady: true}
	if err := Apply(t.Context(), testEnv, plan, fake, fake); !errors.Is(err, ErrVerification) ||
		!strings.Contains(err.Error(), "health probe") {
		t.Fatalf("expected health failure, got %v", err)
	}
	fake.state["remote"] = Observation{Installation: Installation{Present: true}, Healthy: true, AuthReady: true}
	if err := Apply(t.Context(), testEnv, plan, fake, nil); err != nil {
		t.Fatal(err)
	}
	delete(fake.state, "local")
	if err := Apply(t.Context(), testEnv, plan, fake, fake); !errors.Is(err, ErrVerification) || len(fake.installs) != 0 {
		t.Fatalf("verification-only step attempted installation: error %v, installs %v", err, fake.installs)
	}
}

func TestRejectsBadArtifactMetadataBeforeProbing(t *testing.T) {
	for _, bad := range []Artifact{
		{Platform: testEnv.Platform, Source: "http://example.com", SHA256: artifact(1).SHA256},
		{Platform: testEnv.Platform, Source: "https://example.com", SHA256: "invalid"},
		{Platform: testEnv.Platform, Source: "https://user:password@example.com", SHA256: artifact(1).SHA256},
	} {
		fake := &fakeEnvironment{}
		_, err := BuildPlan(t.Context(), testEnv, []Manifest{{"provider", []Companion{cli("local", bad)}}}, fake)
		if err == nil || len(fake.probes) != 0 {
			t.Fatalf("invalid artifact reached planning: error %v, probes %v", err, fake.probes)
		}
	}
}
