package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nscaledev/devflow/internal/release"
)

// UnitPlan is the version state of one deployable unit.
type UnitPlan struct {
	Unit        release.Unit `json:"unit"`
	LastTag     string       `json:"last_tag,omitempty"`
	Current     string       `json:"current"`
	Bump        string       `json:"bump"`
	Next        string       `json:"next"`
	Version     string       `json:"version"` // what a build of this branch is versioned as
	Branch      string       `json:"branch"`
	BranchKind  string       `json:"branch_kind"`
	Commits     int          `json:"commits_since_tag"`
	WillRelease bool         `json:"will_release_on_merge"`
}

// ReleasePlan computes the next version of every unit for the current branch.
func (a *App) ReleasePlan(ctx context.Context, unit string) ([]UnitPlan, error) {
	cfg, err := release.Load(a.Repo.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("no .devflow/releases.yml: run devflow release init")
	}
	if err != nil {
		return nil, err
	}
	def := cfg.DefaultBranch
	if def == "" {
		def = a.Repo.DefaultBranch(ctx)
	}
	branch := a.Repo.Branch(ctx)
	kind := release.BranchKind(cfg.Strategy, branch, def)
	sha, _ := a.Repo.Git(ctx, "rev-parse", "--short=7", "HEAD")
	var plans []UnitPlan
	for _, u := range cfg.Units {
		if unit != "" && u.Name != unit {
			continue
		}
		p := UnitPlan{Unit: u, Branch: branch, BranchKind: kind, Current: "0.0.0"}
		prefix := cfg.TagPrefix(u)
		cur := release.Version{}
		if out, err := a.Repo.Git(ctx, "describe", "--tags", "--abbrev=0", "--match", prefix+"[0-9]*.[0-9]*.[0-9]*", "HEAD"); err == nil {
			p.LastTag = strings.TrimSpace(out)
			if v, ok := release.ParseTagVersion(p.LastTag, prefix); ok {
				cur = v
				p.Current = v.String()
			}
		}
		rng := "HEAD"
		if p.LastTag != "" {
			rng = p.LastTag + "..HEAD"
		}
		args := []string{"log", "--format=%B%x00", rng}
		if u.Path != "." {
			args = append(args, "--", u.Path)
		}
		out, _ := a.Repo.Git(ctx, args...)
		var msgs []string
		for _, m := range strings.Split(out, "\x00") {
			if m = strings.TrimSpace(m); m != "" {
				msgs = append(msgs, m)
			}
		}
		p.Commits = len(msgs)
		p.Bump = release.BumpFor(msgs)
		next := release.Next(cur, p.Bump)
		if p.LastTag == "" && p.Bump == release.BumpNone && len(msgs) > 0 {
			next = release.Version{Major: 0, Minor: 1, Patch: 0} // first release
		}
		p.Next = next.String()
		p.WillRelease = next != cur
		if kind == "default" {
			p.Version = p.Next
		} else {
			p.Version = release.Prerelease(next, kind, len(msgs), strings.TrimSpace(sha))
		}
		plans = append(plans, p)
	}
	if unit != "" && len(plans) == 0 {
		return nil, errors.New("no unit named " + strconv.Quote(unit))
	}
	return plans, nil
}

// ReleaseInitResult lists what release init did.
type ReleaseInitResult struct {
	Strategy string         `json:"strategy"`
	Units    []release.Unit `json:"units"`
	Written  []string       `json:"written"`
	Notes    []string       `json:"notes,omitempty"`
}

// ReleaseInit detects deployable units and the branching strategy, then writes
// .devflow/releases.yml and the release-please files. Existing releases.yml is kept
// unless force is set.
func (a *App) ReleaseInit(ctx context.Context, strategy string, force bool) (*ReleaseInitResult, error) {
	res := &ReleaseInitResult{}
	cfg, err := release.Load(a.Repo.Dir)
	switch {
	case err == nil && !force:
		res.Notes = append(res.Notes, ".devflow/releases.yml exists; kept it (use --force to re-detect)")
	case err == nil || errors.Is(err, os.ErrNotExist):
		owner := ""
		if r := a.Repo.RemoteRepo(ctx); r != "" {
			owner = strings.Split(r, "/")[0]
		}
		cfg = &release.Config{Strategy: strategy, Units: release.Detect(a.Repo.Dir, owner)}
		if cfg.Strategy == "" {
			cfg.Strategy = a.detectStrategy(ctx)
		}
		if len(cfg.Units) == 0 {
			name := filepath.Base(a.Repo.Dir)
			cfg.Units = []release.Unit{{Name: strings.ToLower(name), Path: ".", ReleaseType: "simple", Builds: []string{release.Binary}}}
			res.Notes = append(res.Notes, "no deployables detected; added the repo root as one unit; edit .devflow/releases.yml")
		}
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		if err := cfg.Save(a.Repo.Dir); err != nil {
			return nil, err
		}
		res.Written = append(res.Written, ".devflow/releases.yml")
	default:
		return nil, err
	}
	written, err := a.ReleaseSync(ctx, cfg)
	res.Written = append(res.Written, written...)
	res.Strategy, res.Units = cfg.Strategy, cfg.Units
	res.Notes = append(res.Notes,
		"stable releases are created automatically on "+a.Repo.DefaultBranch(ctx)+" from feat/fix/! commits",
		"commit these files; the devflow caller workflow runs nscaledev/devflow/.github/workflows/semver-release.yml")
	return res, err
}

func (a *App) detectStrategy(ctx context.Context) string {
	for _, ref := range []string{"develop", "origin/develop"} {
		if _, err := a.Repo.Git(ctx, "rev-parse", "--verify", "--quiet", ref); err == nil {
			return release.GitFlow
		}
	}
	return release.Trunk
}

// ReleaseSync regenerates release-please-config.json and .release-please-manifest.json
// from releases.yml, keeping already-released versions.
func (a *App) ReleaseSync(ctx context.Context, cfg *release.Config) ([]string, error) {
	if cfg == nil {
		var err error
		if cfg, err = release.Load(a.Repo.Dir); err != nil {
			return nil, err
		}
	}
	existing := map[string]string{}
	manifestPath := filepath.Join(a.Repo.Dir, ".release-please-manifest.json")
	if b, err := os.ReadFile(manifestPath); err == nil {
		_ = json.Unmarshal(b, &existing)
	}
	// Seed from existing tags so adoption doesn't restart at 0.0.0.
	for _, u := range cfg.Units {
		if existing[u.Path] != "" {
			continue
		}
		prefix := cfg.TagPrefix(u)
		if out, err := a.Repo.Git(ctx, "describe", "--tags", "--abbrev=0", "--match", prefix+"[0-9]*.[0-9]*.[0-9]*"); err == nil {
			if v, ok := release.ParseTagVersion(strings.TrimSpace(out), prefix); ok {
				existing[u.Path] = v.String()
			}
		}
	}
	files := map[string]any{
		"release-please-config.json":    cfg.PleaseConfig(),
		".release-please-manifest.json": cfg.Manifest(existing),
	}
	var written []string
	for name, v := range files {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return written, err
		}
		if err := os.WriteFile(filepath.Join(a.Repo.Dir, name), append(b, '\n'), 0o644); err != nil {
			return written, err
		}
		written = append(written, name)
	}
	return written, nil
}
