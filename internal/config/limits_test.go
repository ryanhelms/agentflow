package config_test

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/commit"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/update"
)

// The limits in the settings catalog must never reject a value the code
// accepts: an existing configuration would show an error after an upgrade.
// These tests run each limit through the code that reads the field.

func inspect(t *testing.T, yaml string) error {
	t.Helper()
	_, _, err := config.InspectRepo([]byte(yaml), nil)
	return err
}

func TestStepperMinimumsAreAcceptedByTheCode(t *testing.T) {
	t.Setenv("AGENTFLOW_HOME", t.TempDir())

	checks := map[string]func(t *testing.T, min int){
		// A pull request title is checked against subject_max minus the squash
		// suffix. min is the smallest value for which the shortest conventional
		// title ("ci: a") can pass; one less leaves no title that can.
		"repo:commit.subject_max": func(t *testing.T, min int) {
			const shortest = "ci: a"
			if want := commit.SquashSuffixReserve + len(shortest); min != want {
				t.Errorf("min = %d, want SquashSuffixReserve + len(%q) = %d", min, shortest, want)
			}
			keyless := false
			rules := func(max int) config.Commit {
				return config.Commit{Types: config.DefaultTypes, SubjectMax: max, RequireKey: &keyless}
			}
			if issues := commit.ValidatePRTitle(shortest, rules(min), ""); len(issues) != 0 {
				t.Errorf("a pull request title %q at min %d has issues: %v", shortest, min, issues)
			}
			if issues := commit.Validate(shortest, rules(min), ""); len(issues) != 0 {
				t.Errorf("a commit subject %q at min %d has issues: %v", shortest, min, issues)
			}
			if issues := commit.ValidatePRTitle(shortest, rules(min-1), ""); len(issues) == 0 {
				t.Errorf("min %d is not the smallest value that works: %d also accepts %q", min, min-1, shortest)
			}
			if err := inspect(t, "commit:\n  subject_max: "+strconv.Itoa(min)+"\n"); err != nil {
				t.Errorf("the loader rejects min %d: %v", min, err)
			}
		},
		// The loader rejects a negative limit and accepts zero, which means the default.
		"repo:task_management.settings.search.limit|github-issues": func(t *testing.T, min int) {
			config := func(n int) string {
				return "task_management:\n  provider: github-issues\n  settings:\n    search:\n      limit: " + strconv.Itoa(n) + "\n"
			}
			if err := inspect(t, config(min)); err != nil {
				t.Errorf("the loader rejects min %d: %v", min, err)
			}
			if err := inspect(t, config(min-1)); err == nil {
				t.Errorf("min %d is not the smallest value that works: the loader accepts %d", min, min-1)
			}
		},
		// A release tag cannot have a negative major version.
		"repo:update.track": func(t *testing.T, min int) {
			if err := inspect(t, "update:\n  track: "+strconv.Itoa(min)+"\n"); err != nil {
				t.Errorf("the loader rejects min %d: %v", min, err)
			}
			if v, ok := update.ParseVersion("v" + strconv.Itoa(min) + ".0.0"); !ok || v.Major != min {
				t.Errorf("update.ParseVersion does not read major version %d", min)
			}
			if _, ok := update.ParseVersion("v" + strconv.Itoa(min-1) + ".0.0"); ok {
				t.Errorf("min %d is not the smallest value that works: ParseVersion reads major version %d", min, min-1)
			}
		},
	}

	seen := map[string]bool{}
	for _, f := range config.Catalog() {
		if f.Meta.Widget != config.WidgetStepper {
			continue
		}
		key := f.ID
		if f.Provider != "" {
			key += "|" + f.Provider
		}
		seen[key] = true
		check, ok := checks[key]
		if !ok {
			t.Errorf("%s: a stepper limit needs a check against the code in this test", key)
			continue
		}
		if f.Meta.Min == nil {
			t.Errorf("%s: no min", key)
			continue
		}
		if f.Meta.Step != 1 {
			t.Errorf("%s: step is %d; use 1 unless the code needs more", key, f.Meta.Step)
		}
		if f.Meta.Max != nil {
			t.Errorf("%s: max %d is not checked by the code; a limit the code lacks can reject a value it accepts", key, *f.Meta.Max)
		}
		t.Run(key, func(t *testing.T) { check(t, *f.Meta.Min) })
	}
	for key := range checks {
		if !seen[key] {
			t.Errorf("%s: this test checks a stepper the catalog no longer has", key)
		}
	}
}

// TestDurationLimitsAreTheOnesTheCodeChecks runs the approval window limits
// through the loaders: both accept the limits and reject a value outside them.
func TestDurationLimitsAreTheOnesTheCodeChecks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTFLOW_HOME", home)

	user := func(t *testing.T, window string) error {
		t.Helper()
		if err := os.WriteFile(filepath.Join(home, "config.yml"), []byte("approval_window: "+window+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := config.LoadUser()
		return err
	}
	repo := func(t *testing.T, window string) error {
		t.Helper()
		return inspect(t, "policy:\n  max_approval_window: "+window+"\n")
	}
	load := map[string]func(*testing.T, string) error{"personal:approval_window": user, "repo:policy.max_approval_window": repo}

	seen := map[string]bool{}
	for _, f := range config.Catalog() {
		if f.Meta.Widget != config.WidgetDuration {
			continue
		}
		seen[f.ID] = true
		if f.Meta.MinDuration == "" && f.Meta.MaxDuration == "" {
			continue // no limit: the code checks none
		}
		loader, ok := load[f.ID]
		if !ok {
			t.Errorf("%s: a duration limit needs a check against the code in this test", f.ID)
			continue
		}
		lo, err := time.ParseDuration(f.Meta.MinDuration)
		if err != nil {
			t.Fatalf("%s: min_duration: %v", f.ID, err)
		}
		hi, err := time.ParseDuration(f.Meta.MaxDuration)
		if err != nil {
			t.Fatalf("%s: max_duration: %v", f.ID, err)
		}
		t.Run(f.ID, func(t *testing.T) {
			for _, ok := range []time.Duration{lo, hi} {
				if err := loader(t, ok.String()); err != nil {
					t.Errorf("the loader rejects %s, which the limits allow: %v", ok, err)
				}
			}
			for _, bad := range []time.Duration{lo - time.Second, hi + time.Second} {
				if err := loader(t, bad.String()); err == nil {
					t.Errorf("the loader accepts %s, outside the limits %s to %s", bad, lo, hi)
				}
			}
		})
	}
	for id := range load {
		if !seen[id] {
			t.Errorf("%s: this test checks a duration the catalog no longer has", id)
		}
	}
}
