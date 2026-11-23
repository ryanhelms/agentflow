package webui

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/consolecfg"
	"github.com/nscaledev/devflow/internal/filelock"
)

const approvalConfigFile = ".devflow/config.yml"

type autonomyEdit struct {
	Scope        string                       `json:"scope"`
	Settings     *consolecfg.ApprovalSettings `json:"settings"`
	UserRevision string                       `json:"user_revision"`
	Content      string                       `json:"content"`
	Base         string                       `json:"base_digest"`
}

var errUserRevision = errors.New("personal settings changed; reload them before saving")

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
		return nil, errUserRevision
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
func saveApprovalSettings(dir, revision string, settings consolecfg.ApprovalSettings) ([]byte, error) {
	lock, err := os.OpenFile(filepath.Join(dir, "config.yml.autonomy.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err := filelock.Exclusive(lock); err != nil {
		return nil, err
	}
	raw, err := personalConfig(dir)
	if err != nil {
		return nil, err
	}
	if hash(raw) != revision {
		return nil, errUserRevision
	}
	next, err := consolecfg.EditApprovals(raw, true, settings)
	if err != nil || bytes.Equal(raw, next) {
		return next, err
	}
	file, err := os.CreateTemp(dir, ".autonomy-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err = file.Write(next); err != nil {
		return nil, err
	}
	if err = file.Sync(); err != nil {
		return nil, err
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	if current, err := personalConfig(dir); err != nil || !bytes.Equal(current, raw) {
		return nil, errUserRevision
	}
	if err = os.Rename(file.Name(), filepath.Join(dir, "config.yml")); err != nil {
		return nil, err
	}
	return next, nil
}

func (s *server) autonomy(w http.ResponseWriter, r *http.Request) {
	var change autonomyEdit
	if r.Method == http.MethodPost && !read(w, r, &change) {
		return
	}
	save := r.Method == http.MethodPost && r.URL.Path == "/api/autonomy"
	if save && (change.Settings == nil || (change.Scope != "personal" && change.Scope != "repository")) {
		problem(w, 400, "choose personal or repository settings")
		return
	}
	home, err := config.Home()
	if err != nil {
		problem(w, 500, "cannot open the Devflow settings directory")
		return
	}
	user, err := personalConfig(home)
	if err != nil {
		problem(w, 422, "cannot read the personal configuration file")
		return
	}
	var repo []byte
	if change.Base != "" {
		if _, err := currentFile(r.Context(), s.repo.Dir, approvalConfigFile, change.Base); err != nil {
			problem(w, 409, err.Error())
			return
		}
		repo = []byte(change.Content)
	} else {
		snapshot, err := consolecfg.View(r.Context(), s.repo.Dir)
		if err != nil {
			problem(w, 422, "cannot read repository settings")
			return
		}
		for _, f := range snapshot.Files {
			if f.Path == approvalConfigFile {
				if !f.Editable {
					problem(w, 422, "repository settings are unavailable in the source editor")
					return
				}
				repo = []byte(f.Content)
				break
			}
		}
	}
	if save && change.Scope == "repository" {
		if change.Base == "" {
			problem(w, 409, "reload the repository configuration before staging edits")
			return
		}
		candidate, err := consolecfg.EditApprovals(repo, false, *change.Settings)
		if err != nil {
			problem(w, 422, err.Error())
			return
		}
		plan, err := consolecfg.Preview(r.Context(), s.repo.Dir, approvalConfigFile, string(candidate), change.Base)
		if err != nil {
			problem(w, 422, err.Error())
			return
		}
		view, err := consolecfg.InspectApprovals(user, candidate)
		if err != nil {
			problem(w, 422, err.Error())
			return
		}
		reply(w, 200, map[string]any{"content": string(candidate), "providers": plan.Providers, "autonomy": view})
		return
	}
	view, err := consolecfg.InspectApprovals(user, repo)
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	if !save {
		reply(w, 200, view)
		return
	}
	user, err = saveApprovalSettings(home, change.UserRevision, *change.Settings)
	if errors.Is(err, errUserRevision) {
		problem(w, 409, err.Error())
		return
	}
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	view, err = consolecfg.InspectApprovals(user, repo)
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	reply(w, 200, view)
}
