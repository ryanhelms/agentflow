package configconsole

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/consolecfg"
	"github.com/ryanhelms/agentflow/internal/filelock"
	"github.com/ryanhelms/agentflow/internal/policy"
	"github.com/ryanhelms/agentflow/internal/setup"
)

var ErrUserRevision = errors.New("personal settings changed; reload them before saving")

func personalConfig(dir string) ([]byte, error) {
	path := filepath.Join(dir, "config.yml")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("personal configuration must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	current, err := file.Stat()
	if err != nil || !os.SameFile(info, current) {
		return nil, ErrUserRevision
	}
	const limit = 512 << 10
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if len(data) > limit {
		return nil, errors.New("personal settings exceed the editor limit")
	}
	return data, err
}

// Keep the lock inode stable so every console serializes the revision check
// and atomic replacement. A process exit releases the kernel lock.
func replacePersonal(dir string, original, next []byte) error {
	lock, err := os.OpenFile(filepath.Join(dir, "config.yml.autonomy.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := filelock.Exclusive(lock); err != nil {
		return err
	}
	raw, err := personalConfig(dir)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, original) {
		return ErrUserRevision
	}
	if bytes.Equal(raw, next) {
		return nil
	}
	file, err := os.CreateTemp(dir, ".autonomy-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err = file.Write(next); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if current, err := personalConfig(dir); err != nil || !bytes.Equal(current, raw) {
		return ErrUserRevision
	}
	if err = os.Rename(file.Name(), filepath.Join(dir, "config.yml")); err != nil {
		return err
	}
	return nil
}

// ApprovalPatch updates only supplied fields. An explicitly empty value clears
// that override, restoring inheritance. An omitted action retains its setting.
type ApprovalPatch struct {
	Local                  *config.Level           `json:"local,omitempty" jsonschema:"Local action preference or ceiling. An empty string restores inheritance."`
	Remote                 *config.Level           `json:"remote,omitempty" jsonschema:"Remote action preference or ceiling. An empty string restores inheritance."`
	Window                 *string                 `json:"window,omitempty" jsonschema:"Review window from 1m through 1h. An empty string restores inheritance."`
	Actions                map[string]config.Level `json:"actions,omitempty" jsonschema:"Named action overrides. Set a key to an empty string to remove only that override."`
	AllowAutoPRPublication *bool                   `json:"allow_auto_pr_publication,omitempty" jsonschema:"Repository only. Permit automatic draft or ready pull request publication including its branch push, subject to action rules and opt-in on the target branch. Defaults to false."`
}

func (p ApprovalPatch) apply(old consolecfg.ApprovalSettings) (consolecfg.ApprovalSettings, error) {
	if p.AllowAutoPRPublication != nil {
		old.AllowAutoPRPublication = p.AllowAutoPRPublication
	}
	if p.Local != nil {
		old.Local = *p.Local
	}
	if p.Remote != nil {
		old.Remote = *p.Remote
	}
	if p.Window != nil {
		old.Window = *p.Window
	}
	if len(p.Actions) > 0 {
		old.Actions = maps.Clone(old.Actions)
		if old.Actions == nil {
			old.Actions = map[string]config.Level{}
		}
		for action, level := range p.Actions {
			if !config.ValidAutonomyAction(action) {
				return consolecfg.ApprovalSettings{}, errors.New("choose a supported action and autonomy level")
			}
			if level == "" {
				delete(old.Actions, action)
			} else {
				old.Actions[action] = level
			}
		}
	}
	return old, nil
}

type AutonomyDraft struct {
	Content   string                  `json:"content"`
	Providers *setup.ProviderList     `json:"providers"`
	Autonomy  consolecfg.ApprovalView `json:"autonomy"`
}

type autonomySources struct {
	home       string
	personal   []byte
	repository []byte
}

func loadAutonomy(ctx context.Context, root, base string, content *string) (autonomySources, error) {
	var out autonomySources
	home, err := config.Home()
	if err != nil {
		return out, errors.New("cannot open the Agentflow settings directory")
	}
	out.home = home
	if out.personal, err = personalConfig(home); err != nil {
		return out, errors.New("cannot read the personal configuration file")
	}
	if content != nil && base == "" {
		return out, errors.New("staged repository settings require the source base digest")
	}
	if base != "" {
		file, err := CurrentFile(ctx, root, consolecfg.MainFile, base)
		if err != nil {
			return out, err
		}
		if content != nil {
			// Inspect never echoes arbitrary source, but still bounds parsing of a chat draft.
			if len(*content) > 512<<10 {
				return out, errors.New("candidate exceeds the source editor limit")
			}
			out.repository = []byte(*content)
		} else {
			out.repository = []byte(file.Content)
		}
		return out, nil
	}
	snapshot, err := consolecfg.View(ctx, root)
	if err != nil {
		return out, errors.New("cannot read repository settings")
	}
	for _, f := range snapshot.Files {
		if f.Path == consolecfg.MainFile {
			if !f.Editable {
				return out, errors.New("repository settings are unavailable in the source editor")
			}
			out.repository = []byte(f.Content)
			break
		}
	}
	return out, nil
}

func InspectAutonomy(ctx context.Context, root, base string, content *string) (consolecfg.ApprovalView, error) {
	src, err := loadAutonomy(ctx, root, base, content)
	if err != nil {
		return consolecfg.ApprovalView{}, err
	}
	return consolecfg.InspectApprovals(src.personal, src.repository)
}

func StageAutonomy(ctx context.Context, root, base string, content *string, patch ApprovalPatch) (AutonomyDraft, error) {
	return stageAutonomy(ctx, root, base, content, patch.apply)
}

// StageAutonomyForm accepts the browser's complete repository settings form.
func StageAutonomyForm(ctx context.Context, root, base string, content *string, settings consolecfg.ApprovalSettings) (AutonomyDraft, error) {
	return stageAutonomy(ctx, root, base, content, func(consolecfg.ApprovalSettings) (consolecfg.ApprovalSettings, error) { return settings, nil })
}

func stageAutonomy(ctx context.Context, root, base string, content *string, update func(consolecfg.ApprovalSettings) (consolecfg.ApprovalSettings, error)) (AutonomyDraft, error) {
	var out AutonomyDraft
	if base == "" {
		return out, errors.New("reload the repository configuration before staging edits")
	}
	src, err := loadAutonomy(ctx, root, base, content)
	if err != nil {
		return out, err
	}
	before, err := consolecfg.InspectApprovals(src.personal, src.repository)
	if err != nil {
		return out, err
	}
	settings, err := update(before.Repository)
	if err != nil {
		return out, err
	}
	candidate, err := consolecfg.EditApprovals(src.repository, false, settings)
	if err != nil {
		return out, err
	}
	plan, err := consolecfg.Preview(ctx, root, consolecfg.MainFile, string(candidate), base)
	if err != nil {
		return out, err
	}
	view, err := consolecfg.InspectApprovals(src.personal, candidate)
	if err != nil {
		return out, err
	}
	return AutonomyDraft{Content: string(candidate), Providers: plan.Providers, Autonomy: view}, nil
}

// SavePersonalForm applies an authenticated human browser submission. Agent
// writes go through PersonalAutonomy to capture separately approved intent.
func SavePersonalForm(ctx context.Context, root, base string, content *string, revision string, settings consolecfg.ApprovalSettings) (consolecfg.ApprovalView, error) {
	src, err := loadAutonomy(ctx, root, base, content)
	if err != nil {
		return consolecfg.ApprovalView{}, err
	}
	if _, err := consolecfg.InspectApprovals(src.personal, src.repository); err != nil {
		return consolecfg.ApprovalView{}, err
	}
	if revision == "" || hash(src.personal) != revision {
		return consolecfg.ApprovalView{}, ErrUserRevision
	}
	next, err := consolecfg.EditApprovals(src.personal, true, settings)
	if err != nil {
		return consolecfg.ApprovalView{}, err
	}
	return src.save(next)
}

func (s autonomySources) save(next []byte) (consolecfg.ApprovalView, error) {
	err := replacePersonal(s.home, s.personal, next)
	if errors.Is(err, ErrUserRevision) {
		return consolecfg.ApprovalView{}, ErrUserRevision
	}
	if err != nil {
		return consolecfg.ApprovalView{}, errors.New("could not save the personal configuration file")
	}
	return consolecfg.InspectApprovals(next, s.repository)
}

type personalSave struct {
	consolecfg.ApprovalView
	Note string `json:"note"`
}

// PersonalAutonomy binds the currently reviewed user revision and typed
// changes. Personal source YAML and unrelated preferences never leave disk.
func PersonalAutonomy(ctx context.Context, a *app.App, revision string, patch ApprovalPatch, token string) (any, error) {
	src, err := loadAutonomy(ctx, a.Repo.Dir, "", nil)
	if err != nil {
		return nil, err
	}
	if revision == "" || hash(src.personal) != revision {
		return nil, ErrUserRevision
	}
	view, err := consolecfg.InspectApprovals(src.personal, src.repository)
	if err != nil {
		return nil, err
	}
	settings, err := patch.apply(view.Personal)
	if err != nil {
		return nil, err
	}
	next, err := consolecfg.EditApprovals(src.personal, true, settings)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(next, src.personal) && token == "" {
		return view, nil
	}
	gate, err := CurrentGate(ctx, a.Repo, a.Gate, a.Client)
	if err != nil {
		return nil, err
	}
	summary := struct {
		Scope        string                      `json:"scope"`
		Repository   string                      `json:"repository"`
		UserRevision string                      `json:"user_revision"`
		Before       consolecfg.ApprovalSettings `json:"before"`
		After        consolecfg.ApprovalSettings `json:"after"`
	}{"personal", a.Repo.Dir, revision, view.Personal, settings}
	const action = "console.autonomy"
	d, err := gate.Check(policy.Local, action, summary, token, true)
	if err != nil {
		return nil, err
	}
	if p := app.PendingDecision(gate, d, summary, []string{"agentflow ui"}); p != nil {
		return p, nil
	}
	if token == "" {
		return nil, errors.New("review the personal approval changes before saving")
	}
	out, err := src.save(next)
	if err != nil {
		gate.Audit(policy.Local, action, summary, "error")
		return nil, err
	}
	gate.Audit(policy.Local, action, summary, "saved")
	return personalSave{out, "Restart running Agentflow MCP sessions to use the saved policy for other tools."}, nil
}
