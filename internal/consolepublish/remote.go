package consolepublish

import (
	"context"
	"crypto/sha1" //nolint:gosec // GitHub's Git database currently uses SHA-1 for object IDs.
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/nscaledev/devflow/internal/consolecfg"
	"github.com/nscaledev/devflow/internal/gh"
	"github.com/nscaledev/devflow/internal/setup"
)

const workflow = setup.CallerWorkflow

// blobID computes the Git blob identity used by the repository tree API.
// Comparing these IDs avoids transferring repository file contents into logs.
func blobID(s string) string {
	h := sha1.New() //nolint:gosec // This is the Git object format, not an authentication hash.
	fmt.Fprintf(h, "blob %d%c%s", len(s), 0, s)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// SourceBaseline captures the inputs that the remote default branch must match,
// including an existing caller workflow pin. A missing path maps to an empty ID.
func SourceBaseline(root string, files []consolecfg.File, name, original string) (map[string]string, error) {
	deps := map[string]string{workflow: ""}
	for _, p := range consolecfg.StandardSourcePaths() {
		deps[p] = ""
	}
	editable := false
	for _, f := range files {
		if f.Editable {
			deps[f.Path] = blobID(f.Content)
			if f.Path == name {
				editable = true
			}
		}
	}
	if !editable {
		return nil, errors.New("file is not a repository configuration source")
	}
	deps[name] = blobID(original)
	// The caller workflow controls which devflow version CI will execute.
	d, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	var st fs.FileInfo
	for _, component := range []string{".github", ".github/workflows", workflow} {
		st, err = d.Lstat(component)
		if errors.Is(err, fs.ErrNotExist) {
			return deps, nil
		}
		if err != nil {
			return nil, err
		}
		if st.Mode()&fs.ModeSymlink != 0 {
			return nil, errors.New("cannot verify a symlinked repository workflow")
		}
	}
	if !st.Mode().IsRegular() || st.Size() > 1<<20 {
		return nil, errors.New("cannot verify the repository workflow")
	}
	file, err := d.Open(workflow)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	current, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(st, current) {
		return nil, errors.New("workflow changed while reading")
	}
	data, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, errors.New("cannot verify the repository workflow")
	}
	deps[workflow] = blobID(string(data))
	return deps, nil
}

type remote struct {
	client    *gh.Client
	slug, url string
}

type treeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	SHA  string `json:"sha"`
}

func (a remote) baseline(ctx context.Context, branch string, p proposal, deps map[string]string) (string, bool, error) {
	base, err := a.ref(ctx, branch)
	if err != nil {
		return "", false, err
	}
	var tree struct {
		Truncated bool        `json:"truncated"`
		Tree      []treeEntry `json:"tree"`
	}
	if err := a.client.Do(ctx, "GET", a.url+"/git/trees/"+base+"?recursive=1", nil, &tree); err != nil {
		return "", false, err
	}
	if tree.Truncated {
		return "", false, errors.New("GitHub returned an incomplete tree; cannot verify the default branch configuration")
	}
	files := make(map[string]treeEntry, len(tree.Tree))
	for _, entry := range tree.Tree {
		files[entry.Path] = entry
	}
	current, ok := files[p.Path]
	if ok && (current.Mode == "100644" || current.Mode == "100755") && current.SHA == blobID(p.Content) {
		return base, true, nil
	}
	for name, oid := range deps {
		entry, ok := files[name]
		if ok && entry.Mode != "100644" && entry.Mode != "100755" || entry.SHA != oid {
			return "", false, fmt.Errorf("%s differs from the default branch; refresh your configuration before publishing", name)
		}
	}
	for name := range files {
		if path.Dir(name) == ".devflow/providers" && (path.Ext(name) == ".yml" || path.Ext(name) == ".yaml") && deps[name] == "" {
			return "", false, fmt.Errorf("the default branch has another provider definition %s; refresh before publishing", name)
		}
	}
	return base, false, nil
}

func (a remote) ref(ctx context.Context, branch string) (string, error) {
	var ref struct {
		Ref    string `json:"ref"`
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	err := a.client.Do(ctx, "GET", a.url+"/git/ref/heads/"+url.PathEscape(branch), nil, &ref)
	if err != nil {
		return "", err
	}
	if ref.Ref != "refs/heads/"+branch || ref.Object.SHA == "" {
		return "", errors.New("unexpected GitHub branch reference")
	}
	return ref.Object.SHA, nil
}

func status(err error, code int) bool {
	var api *gh.APIError
	return errors.As(err, &api) && api.Status == code
}

func (a remote) ensureHead(ctx context.Context, base, branch string) (string, error) {
	head, err := a.ref(ctx, branch)
	if status(err, 404) {
		// Create-only; a concurrent retry may win or the response may be lost.
		createErr := a.client.Do(ctx, "POST", a.url+"/git/refs", map[string]string{"ref": "refs/heads/" + branch, "sha": base}, nil)
		head, err = a.ref(ctx, branch)
		if err != nil {
			return "", errors.Join(createErr, err)
		}
	} else if err != nil {
		return "", err
	}
	return head, nil
}

func (a remote) ensureCommit(ctx context.Context, base, branch, head string, p proposal) (string, error) {
	comparison, err := a.compare(ctx, base, head)
	if err != nil {
		return "", err
	}
	if comparison.Ahead == 0 {
		// A previous attempt may have created a branch on an older default.
		// An unrelated advance is fine only if the original file still matches.
		if head != base {
			old, _, err := a.client.File(ctx, a.slug, head, escapePath(p.Path), "")
			if err != nil {
				return "", err
			}
			if string(old) != p.Original {
				return "", errors.New("the existing proposal branch has a different starting configuration")
			}
		}
		input := map[string]any{
			"branch":          map[string]string{"repositoryNameWithOwner": a.slug, "branchName": branch},
			"expectedHeadOid": head,
			"message":         map[string]string{"headline": p.Title, "body": p.body()},
			"fileChanges":     map[string]any{"additions": []map[string]string{{"path": p.Path, "contents": base64.StdEncoding.EncodeToString([]byte(p.Content))}}},
		}
		var written struct {
			CreateCommitOnBranch struct {
				Commit struct {
					OID string `json:"oid"`
				} `json:"commit"`
			} `json:"createCommitOnBranch"`
		}
		writeErr := a.client.GraphQL(ctx, `mutation($input: CreateCommitOnBranchInput!) { createCommitOnBranch(input:$input) { commit { oid } } }`, map[string]any{"input": input}, &written)
		if writeErr == nil && written.CreateCommitOnBranch.Commit.OID != "" {
			return written.CreateCommitOnBranch.Commit.OID, nil
		}
		// Reconcile a raced write, including a response lost after GitHub committed it.
		head, err = a.ref(ctx, branch)
		if err != nil {
			return "", errors.Join(writeErr, err)
		}
		comparison, err = a.compare(ctx, base, head)
		if err != nil || comparison.Ahead == 0 {
			return "", errors.Join(errors.New("could not verify the configuration commit; retry the same edit"), writeErr, err)
		}
	}
	if comparison.Ahead != 1 || len(comparison.Files) != 1 || comparison.Files[0].Filename != p.Path || comparison.Files[0].Status != "modified" || comparison.Files[0].SHA != blobID(p.Content) || len(comparison.Commits) != 1 || !p.matchesMessage(comparison.Commits[0].Commit.Message) {
		return "", errors.New("the existing proposal branch has other changes; inspect its pull request")
	}
	return head, nil
}

type comparison struct {
	Ahead int `json:"ahead_by"`
	Files []struct {
		Filename, Status, SHA string
	} `json:"files"`
	Commits []struct {
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	} `json:"commits"`
}

func (a remote) compare(ctx context.Context, base, head string) (comparison, error) {
	if head == base {
		return comparison{}, nil
	}
	var out comparison
	err := a.client.Do(ctx, "GET", a.url+"/compare/"+url.PathEscape(base)+"..."+url.PathEscape(head), nil, &out)
	return out, err
}

func escapePath(raw string) string {
	parts := strings.Split(raw, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}
