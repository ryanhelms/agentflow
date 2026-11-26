package settings

import (
	"context"
	"sort"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/configconsole"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// DefaultTitle is the publication title when a save gives none.
const DefaultTitle = "chore: update agentflow settings"

// plan is a checked ChangeSet: the preview, and what a save would write.
type plan struct {
	cs      ChangeSet
	files   files
	next    [2]string
	edits   []*edit
	preview Preview
	pub     *configconsole.PublicationPlan
}

func (p *plan) touches(l config.FieldLayer) bool {
	return p.next[layerIndex(l)] != p.files.of(l).Content
}

// Preview checks cs and says what saving it would do. It writes nothing.
func (s *Service) Preview(ctx context.Context, cs ChangeSet) (Preview, error) {
	p, err := s.plan(ctx, cs, nil)
	if err != nil {
		return Preview{}, err
	}
	return p.preview, nil
}

// plan runs the check chain over cs and computes the preview. extra are
// diagnostics a caller adds (undo's published).
func (s *Service) plan(ctx context.Context, cs ChangeSet, extra func(c *check)) (*plan, error) {
	f, err := s.read()
	if err != nil {
		return nil, err
	}
	c := &check{s: s, ctx: ctx, cs: cs, files: f, ev: s.evaluate(ctx, f.personal.Content, f.repo.Content)}
	for i, e := range cs.Edits {
		c.edits = append(c.edits, &edit{Edit: e, index: i})
	}
	for _, l := range chain {
		l(c)
	}
	if extra != nil {
		extra(c)
	}
	p := &plan{cs: cs, files: f, next: c.next, edits: c.edits}
	pv := Preview{Status: StatusOK, Base: f.revisions(), Changes: []FieldChange{}, Effects: []Effect{}, Files: []FileChange{},
		Diagnostics: c.diags, Confirm: []Confirmation{}}
	if pv.Diagnostics == nil {
		pv.Diagnostics = []Diagnostic{}
	}
	s.describe(ctx, c, p, &pv)
	pv.Status = status(pv)
	if pv.Status == StatusOK && p.touches(config.FieldRepo) {
		s.repoPlan(ctx, p, &pv)
		pv.Status = status(pv)
	}
	p.preview = pv
	return p, nil
}

// status derives a preview's status from its diagnostics and changes.
func status(pv Preview) Status {
	conflict, invalid := false, false
	for _, d := range pv.Diagnostics {
		if d.Severity != SeverityError {
			continue
		}
		if d.Code == CodeConflict {
			conflict = true
		} else {
			invalid = true
		}
	}
	switch {
	case invalid:
		return StatusInvalid
	case conflict:
		return StatusConflict
	case len(pv.Changes) == 0:
		return StatusUnchanged
	}
	return StatusOK
}

// describe fills the changes, effects, files and confirmations.
func (s *Service) describe(ctx context.Context, c *check, p *plan, pv *Preview) {
	before := s.values(c.ev)
	after := before
	if p.next != [2]string{f0(p), f1(p)} {
		after = s.values(s.evaluate(ctx, p.next[0], p.next[1]))
	}
	changed := map[valueKey]bool{}
	for _, e := range c.edits {
		if e.failed || !e.known || !p.touches(e.layer) {
			continue
		}
		key := valueKey{e.field.ID, e.field.Provider}
		if changed[key] {
			if e.loosens != "" {
				markLoosens(pv, key)
			}
			continue
		}
		b, a := before[key], after[key]
		if sameValue(b.Layer, a.Layer) {
			continue
		}
		changed[key] = true
		fc := FieldChange{ID: e.field.ID, Provider: e.field.Provider, Edit: e.index, Before: b.Layer, After: a.Layer,
			Applies: e.field.Meta.Applies, Loosens: e.loosens != ""}
		if !sameValue(b.Effective, a.Effective) {
			fc.Effect = &Effect{ID: e.field.ID, Before: b.Effective, After: a.Effective, SourceBefore: b.Source, SourceAfter: a.Source}
		}
		if fc.Applies == config.AppliesRestart {
			pv.Restart = true
		}
		pv.Changes = append(pv.Changes, fc)
	}
	for _, e := range c.edits {
		if e.loosens != "" && !e.failed {
			pv.Confirm = append(pv.Confirm, Confirmation{ID: e.ID, Reason: CodeLoosens, Message: e.loosens})
		}
	}
	keys := make([]valueKey, 0, len(after))
	for k := range after {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].id != keys[j].id {
			return keys[i].id < keys[j].id
		}
		return keys[i].provider < keys[j].provider
	})
	for _, k := range keys {
		b, a := before[k], after[k]
		if changed[k] || sameValue(b.Effective, a.Effective) && b.Active == a.Active {
			continue
		}
		if !b.Active && !a.Active {
			continue
		}
		pv.Effects = append(pv.Effects, Effect{ID: k.id, Before: b.Effective, After: a.Effective, SourceBefore: b.Source, SourceAfter: a.Source})
	}
	if len(pv.Changes) == 0 {
		return
	}
	if p.touches(config.FieldRepo) {
		diff := c.planDiff
		for _, e := range c.edits {
			if e.known && e.field.Meta.Sensitive {
				diff = "" // a sensitive value never leaves the disk, not even in a diff
			}
		}
		pv.Files = append(pv.Files, FileChange{Layer: config.FieldRepo, Path: RepoFile, Before: p.files.repo.Revision,
			After: Revision(p.next[1], true), Diff: diff})
	}
	if p.touches(config.FieldPersonal) {
		pv.Files = append(pv.Files, FileChange{Layer: config.FieldPersonal, Path: PersonalFile, Before: p.files.personal.Revision,
			After: Revision(p.next[0], true)})
	}
}

func f0(p *plan) string { return p.files.personal.Content }
func f1(p *plan) string { return p.files.repo.Content }

func markLoosens(pv *Preview, key valueKey) {
	for i := range pv.Changes {
		if pv.Changes[i].ID == key.id && pv.Changes[i].Provider == key.provider {
			pv.Changes[i].Loosens = true
		}
	}
}

// repoPlan fills the repository half: the mode, and for worktree the
// protected-branch check, for draft and direct the publication.
func (s *Service) repoPlan(ctx context.Context, p *plan, pv *Preview) {
	mode := p.cs.Repo.Mode
	if mode == "" {
		mode = RepoDraft
	}
	rp := &RepoPlan{Mode: mode, TaskRef: p.cs.Repo.TaskRef}
	pv.Repo = rp
	if mode == RepoWorktree {
		rp.Branch = s.branch(ctx)
		if protectedBranch(rp.Branch, p.files.repo.Content, p.next[1]) {
			pv.Diagnostics = append(pv.Diagnostics, Diagnostic{Severity: SeverityError, Edit: -1, Code: CodeProtected,
				Message: "This worktree is on a protected branch. Save to a pull request instead, or switch to a task branch."})
		}
		return
	}
	rp.Title = p.cs.Repo.Title
	if rp.Title == "" {
		rp.Title = DefaultTitle
	}
	pub, err := s.Publisher.Plan(ctx, s.Repo, RepoFile, p.files.repo.Content, p.next[1], rp.Title)
	if err != nil {
		rp.Warning = safeMessage(err)
		pv.Diagnostics = append(pv.Diagnostics, Diagnostic{Severity: SeverityError, Edit: -1, Code: CodeReference,
			Message: "Cannot publish this change: " + rp.Warning})
		return
	}
	rp.Destination, rp.CI = pub.Destination, pub.CI
	p.pub = &pub
}

// protectedBranch reports whether branch is protected under the repository
// policy, in the current file or the new one.
func protectedBranch(branch string, files ...string) bool {
	if branch == "" {
		return true // detached: nothing to commit the change to
	}
	for _, content := range files {
		cfg, _, err := config.ParseRepoCore([]byte(content))
		if err != nil || policy.IsProtected(branch, cfg.Policy.ProtectedBranches) {
			return true
		}
	}
	return false
}
