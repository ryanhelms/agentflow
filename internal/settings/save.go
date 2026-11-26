package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/configconsole"
	"github.com/ryanhelms/agentflow/internal/filelock"
	"github.com/ryanhelms/agentflow/internal/gitx"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// Gate gates a save once: (*app.App).GateOperation.
type Gate func(app.OperationGate) (*app.Pending, error)

// Publisher publishes the repository half of a draft or direct save (one of
// the writers of the Strategy). DefaultPublisher uses the console publication
// flow of agentflow_config_apply; tests replace it.
type Publisher interface {
	// Plan checks the candidate and resolves what publishing binds.
	Plan(ctx context.Context, repo *gitx.Repo, path, original, content, title string) (configconsole.PublicationPlan, error)
	// Publish opens (draft) or applies (direct) the pull request. It never
	// touches the worktree.
	Publish(ctx context.Context, repo *gitx.Repo, plan configconsole.PublicationPlan, path, original, content string, draft bool, taskRef string) (Publication, error)
	// State is the pull request's state on GitHub: open, merged or closed.
	State(ctx context.Context, repo *gitx.Repo, pub Publication) (string, error)
}

// DefaultPublisher publishes through consolepublish.
var DefaultPublisher Publisher = consolePublisher{}

type consolePublisher struct{}

func (consolePublisher) Plan(ctx context.Context, repo *gitx.Repo, path, original, content, title string) (configconsole.PublicationPlan, error) {
	return configconsole.PlanPublication(ctx, repo, path, original, content, title)
}

func (consolePublisher) Publish(ctx context.Context, repo *gitx.Repo, plan configconsole.PublicationPlan, path, original, content string, draft bool, taskRef string) (Publication, error) {
	res, err := configconsole.Publish(ctx, repo, plan, path, original, content, draft, taskRef, nil)
	if err != nil {
		return Publication{}, err
	}
	mode := RepoDirect
	if draft {
		mode = RepoDraft
	}
	return Publication{Mode: mode, URL: res.URL, Number: pullNumber(res.URL), Branch: res.Branch, State: res.State}, nil
}

func (consolePublisher) State(ctx context.Context, repo *gitx.Repo, pub Publication) (string, error) {
	slug, number, ok := pullOf(pub.URL)
	if !ok {
		return "", errors.New("the saved pull request has no GitHub address")
	}
	client, err := (&app.App{Repo: repo}).GitHub(ctx)
	if err != nil {
		return "", err
	}
	var pr struct {
		State  string `json:"state"`
		Merged bool   `json:"merged"`
	}
	if err := client.Do(ctx, "GET", fmt.Sprintf("repos/%s/pulls/%d", slug, number), nil, &pr); err != nil {
		return "", err
	}
	if pr.Merged {
		return "merged", nil
	}
	return pr.State, nil
}

// pullOf parses https://github.com/OWNER/REPO/pull/N.
func pullOf(url string) (string, int, bool) {
	rest, ok := strings.CutPrefix(url, "https://github.com/")
	if !ok {
		return "", 0, false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 4 || parts[2] != "pull" {
		return "", 0, false
	}
	n, err := strconv.Atoi(parts[3])
	return parts[0] + "/" + parts[1], n, err == nil && n > 0
}

func pullNumber(url string) int {
	_, n, _ := pullOf(url)
	return n
}

// Save checks cs, gates it once and, on confirmation, writes every layer or
// none, then records the journal (config.set). A refusal returns the Preview
// (status invalid, conflict or unchanged); an unconfirmed call returns the
// gate's Pending; an applied save returns a SaveResult.
func (s *Service) Save(ctx context.Context, cs ChangeSet, token string, gate Gate) (any, error) {
	p, err := s.plan(ctx, cs, nil)
	if err != nil {
		return nil, err
	}
	return s.commit(ctx, p, "config.set", "", token, gate)
}

// commit gates a checked plan and applies it.
func (s *Service) commit(ctx context.Context, p *plan, operation, undoOf, token string, gate Gate) (any, error) {
	if p.preview.Status != StatusOK {
		return p.preview, nil
	}
	class, action, related := p.gate()
	pend, err := gate(app.OperationGate{Class: class, Action: action, Related: related, Summary: p.summary(operation),
		Token: token, RequireConfirmation: true, Commands: []string{"agentflow ui"}})
	if err != nil {
		return nil, err
	}
	if pend != nil {
		return pend, nil
	}
	id, _ := policy.OperationIDOf(token)
	return s.apply(ctx, p, id, undoOf)
}

// mode is the repository mode of the plan.
func (p *plan) mode() RepoMode {
	if p.cs.Repo.Mode == "" {
		return RepoDraft
	}
	return p.cs.Repo.Mode
}

// gate resolves the class and action of the save (ADR, "Phase 4 save").
//
// A save that changes personal approval settings (autonomy.* or
// approval_window) or loosens policy anywhere also answers to
// console.autonomy, the action agentflow_config_apply gates personal
// approval changes with: the strictest of the related rules wins, so
// console.autonomy: suggest, personal or as a repository ceiling, cannot
// be bypassed through the settings editor.
func (p *plan) gate() (policy.Class, string, []string) {
	var related []string
	if p.touches(config.FieldRepo) && p.mode() != RepoWorktree {
		if p.touches(config.FieldPersonal) {
			related = append(related, "console.settings")
		}
		if p.approvalChange() {
			related = append(related, "console.autonomy")
		}
		return policy.Remote, "console.publish", related
	}
	if p.approvalChange() {
		related = append(related, "console.autonomy")
	}
	return policy.Local, "console.settings", related
}

// approvalChange reports whether the plan changes a personal approval
// setting or loosens policy.
func (p *plan) approvalChange() bool {
	if len(p.preview.Confirm) > 0 {
		return true
	}
	for _, c := range p.preview.Changes {
		if approvalField(c.ID) {
			return true
		}
	}
	return false
}

// approvalField reports whether id is a personal approval setting.
func approvalField(id string) bool {
	path, ok := strings.CutPrefix(id, "personal:")
	if !ok {
		return false
	}
	return path == "approval_window" || path == "autonomy" || strings.HasPrefix(path, "autonomy.")
}

// gateSummary is the payload bound into the approval: any difference
// between the preview and the save makes the token fail with
// operation_changed. The confirmations come first.
type gateSummary struct {
	Operation string          `json:"operation"`
	Confirm   []Confirmation  `json:"confirm"`
	Changes   []summaryChange `json:"changes"`
	Base      Revisions       `json:"base"`
	After     Revisions       `json:"after"`
	Repo      *summaryRepo    `json:"repo,omitempty"`
}

type summaryChange struct {
	ID     string `json:"id"`
	Before Value  `json:"before"`
	After  Value  `json:"after"`
}

type summaryRepo struct {
	Mode        RepoMode          `json:"mode"`
	Title       string            `json:"title,omitempty"`
	TaskRef     string            `json:"task_ref,omitempty"`
	Branch      string            `json:"branch,omitempty"`
	Destination string            `json:"destination,omitempty"`
	Head        string            `json:"head,omitempty"`
	Baseline    map[string]string `json:"baseline,omitempty"`
}

func (p *plan) summary(operation string) gateSummary {
	pv := p.preview
	out := gateSummary{Operation: operation, Confirm: pv.Confirm, Base: pv.Base, Changes: []summaryChange{}}
	for _, c := range pv.Changes {
		out.Changes = append(out.Changes, summaryChange{ID: c.ID, Before: c.Before, After: c.After})
	}
	for _, f := range pv.Files {
		if f.Layer == config.FieldPersonal {
			out.After.Personal = f.After
		} else {
			out.After.Repo = f.After
		}
	}
	if pv.Repo != nil {
		r := &summaryRepo{Mode: pv.Repo.Mode, Title: pv.Repo.Title, TaskRef: pv.Repo.TaskRef, Branch: pv.Repo.Branch}
		if p.pub != nil {
			r.Destination, r.Head, r.Baseline = p.pub.Destination, p.pub.Head, p.pub.Baseline
		}
		out.Repo = r
	}
	return out
}

// apply writes the plan: under the personal file lock, the revisions bound
// by the review must still hold; the repository half publishes or writes the
// worktree file first, then the personal file, then the journal.
func (s *Service) apply(ctx context.Context, p *plan, id, undoOf string) (any, error) {
	if err := os.MkdirAll(s.Home, 0o700); err != nil {
		return nil, errors.New("cannot open the agentflow settings directory")
	}
	lock, err := os.OpenFile(filepath.Join(s.Home, "config.yml.autonomy.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, errors.New("cannot lock the personal settings")
	}
	defer lock.Close()
	if err := filelock.Exclusive(lock); err != nil {
		return nil, errors.New("cannot lock the personal settings")
	}
	now, err := s.read()
	if err != nil {
		return nil, err
	}
	if now.revisions() != p.files.revisions() {
		return nil, fmt.Errorf("a settings file changed after the review: %w", policy.ErrChanged)
	}
	res := SaveResult{Status: StatusSaved, Operation: id, Changes: p.preview.Changes, Restart: p.preview.Restart}
	var pub *Publication
	repoDone := false
	if p.touches(config.FieldRepo) {
		switch p.mode() {
		case RepoWorktree:
			branch := s.branch(ctx)
			if protectedBranch(branch, now.repo.Content, p.next[1]) {
				return nil, fmt.Errorf("the worktree is now on a protected branch: %w", policy.ErrChanged)
			}
			if err := writeAtomic(now.repo.abs, p.next[1], 0); err != nil {
				return nil, errors.New("could not write " + RepoFile)
			}
			pub = &Publication{Mode: RepoWorktree, Branch: branch}
		default:
			if p.pub == nil {
				return nil, errors.New("the publication was not planned")
			}
			out, err := s.Publisher.Publish(ctx, s.Repo, *p.pub, RepoFile, now.repo.Content, p.next[1], p.mode() == RepoDraft, p.cs.Repo.TaskRef)
			if err != nil {
				return nil, err
			}
			pub = &out
		}
		repoDone = true
		res.Publication = pub
	}
	personalDone := false
	if p.touches(config.FieldPersonal) {
		if err := writeAtomic(now.personal.abs, p.next[0], 0o600); err != nil {
			if !repoDone {
				return nil, errors.New("could not write the personal settings")
			}
			res.Status, res.Error = StatusPartial, "The repository change was saved, but the personal settings could not be written."
		} else {
			personalDone = true
		}
	}
	after, err := s.read()
	if err == nil {
		res.Revisions = after.revisions()
	}
	rec := JournalRecord{Version: 1, Operation: id, Time: s.now().UTC(), Client: s.Client, Before: p.files.revisions(),
		After: res.Revisions, Publication: pub, UndoOf: undoOf, Edits: []JournalEdit{}}
	for _, e := range p.edits {
		if e.failed || e.layer == "" || e.beforeSet == e.afterSet && string(canonical(e.before)) == string(canonical(e.after)) {
			continue
		}
		if e.layer == config.FieldPersonal && !personalDone || e.layer == config.FieldRepo && !repoDone {
			continue
		}
		rec.Edits = append(rec.Edits, JournalEdit{ID: e.ID, Before: orNull(e.before, e.beforeSet), After: orNull(e.after, e.afterSet)})
	}
	if len(rec.Edits) > 0 {
		res.Undo = s.writeJournal(ctx, rec) == nil
	}
	return res, nil
}

func orNull(raw json.RawMessage, set bool) json.RawMessage {
	if !set {
		return json.RawMessage("null")
	}
	return canonical(raw)
}

// writeAtomic replaces path with content: a temporary file in the same
// directory, fsync, rename. perm 0 keeps the current file's mode.
func writeAtomic(path, content string, perm os.FileMode) error {
	if perm == 0 {
		perm = 0o644
		if info, err := os.Stat(path); err == nil {
			perm = info.Mode().Perm()
		}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".agentflow-settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return err
	}
	if err := f.Chmod(perm); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
