// Package consolecfg inspects repository-owned configuration and validates proposed edits.
// Reading and previewing a draft do not run commands configured by the repository.
package consolecfg

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/tasktemplate"
	"gopkg.in/yaml.v3"
)

const (
	MainFile          = ".agentflow/config.yml"
	definitionsDir    = ".agentflow/providers"
	maxSourceBytes    = 512 << 10
	maxInventoryBytes = 8 << 20
)

type File struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	Editable bool   `json:"editable"`
	Content  string `json:"content,omitempty"`
	Digest   string `json:"digest,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type source struct{ name, kind string }

const (
	core    = "Repository settings"
	def     = "Provider definition"
	units   = "Release units"
	flow    = "Flow map"
	catalog = "Task templates"
	docs    = "Document mappings"
	envs    = "Deployment environments"
	managed = "Managed file"
)

var standard = []source{
	{MainFile, core}, {".agentflow/releases.yml", units},
	{"flow-map.yml", flow}, {".agentflow/task-templates.json", catalog},
	{"docs/confluence.yml", docs}, {".jira/config.yml", envs},
	{".github/workflows/agentflow.yml", managed}, {".release-please-manifest.json", managed},
	{"release-please-config.json", managed}, {".claude/settings.json", managed},
	{".claude/settings.local.json", managed}, {".codex/config.toml", managed},
	{".cursor/mcp.json", managed}, {".gemini/settings.json", managed},
	{".grok/settings.json", managed}, {".vscode/mcp.json", managed}, {".mcp.json", managed},
}

// StandardSourcePaths lists registered configuration sources, including paths
// that may exist only on the remote branch when publishing a local edit.
func StandardSourcePaths() []string {
	var paths []string
	for _, source := range standard {
		if source.kind != managed {
			paths = append(paths, source.name)
		}
	}
	return paths
}

var credentialAssignment = regexp.MustCompile(`(?im)(^\s*|[{,]\s*)["']?(password|passwd|secret|api[_-]?key|(?:access[_-]?|refresh[_-]?)?token|client[_-]?secret|webhook[_-]?url|private[_-]?key)["']?\s*[:=]\s*[^\s#}]`)
var credentialURL = regexp.MustCompile(`(?i)https?://[^/\s?#]+:[^/\s?#]+@|[?&](token|access_token|key|signature|sig)=[^&\s]+`)

func sensitive(text string) bool {
	return app.ContainsSecret(text) || credentialAssignment.MatchString(text) || credentialURL.MatchString(text)
}

func digest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

type inventory struct {
	files    []File
	byName   map[string]int
	bytes    int
	problems []string
}

// regular checks every component. Root also confines opens across races; a link
// to a different file in this checkout is not an editable config either.
func regular(root *os.Root, name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\:") || strings.ContainsFunc(name, unicode.IsControl) {
		return nil, errors.New("invalid repository path")
	}
	var prefix string
	var st fs.FileInfo
	for component := range strings.SplitSeq(name, "/") {
		prefix = path.Join(prefix, component)
		var err error
		st, err = root.Lstat(prefix)
		if err != nil {
			return nil, err
		}
		if st.Mode()&fs.ModeSymlink != 0 {
			return nil, errors.New("symbolic links cannot be edited")
		}
	}
	return st, nil
}

func (inv *inventory) add(root *os.Root, s source, custom bool) error {
	if _, ok := inv.byName[s.name]; ok {
		return nil
	}
	st, err := regular(root, s.name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	f := File{Path: s.name, Kind: s.kind}
	switch {
	case err != nil:
		f.Reason = err.Error()
	case !st.Mode().IsRegular():
		f.Reason = "Only regular files can be inspected"
	case s.kind == managed:
		f.Reason = "Managed by its owning workflow or client"
	case st.Size() > maxSourceBytes:
		f.Reason = "File exceeds the source editor limit"
	default:
		fd, err := root.Open(s.name)
		if err != nil {
			return err
		}
		defer fd.Close()
		now, err := fd.Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(st, now) {
			return fmt.Errorf("%s changed while reading", s.name)
		}
		data, err := io.ReadAll(io.LimitReader(fd, maxSourceBytes+1))
		if err != nil {
			return err
		}
		if len(data) > maxSourceBytes {
			return fmt.Errorf("%s exceeds the source editor limit", s.name)
		}
		inv.bytes += len(data)
		if inv.bytes > maxInventoryBytes {
			return errors.New("configuration exceeds the inventory limit")
		}
		switch {
		case !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0:
			f.Reason = "Binary content is unavailable in the source editor"
		case sensitive(string(data)):
			f.Reason = "Content withheld because it may contain credentials"
		case custom && !matchesSource(s.kind, data):
			f.Reason = "The referenced file does not match the expected configuration format"
		default:
			f.Editable = true
			f.Content = string(data)
			f.Digest = digest(f.Content)
		}
	}
	inv.byName[s.name] = len(inv.files)
	inv.files = append(inv.files, f)
	return nil
}

func matchesSource(kind string, data []byte) bool {
	if kind == catalog {
		_, err := tasktemplate.DecodeCatalog(strings.NewReader(string(data)))
		return err == nil
	}
	var fields map[string]yaml.Node
	if yaml.Unmarshal(data, &fields) != nil {
		return false
	}
	for k := range fields {
		if k != "version" && k != "defaults" && k != "projects" {
			return false
		}
	}
	_, hasDefaults := fields["defaults"]
	_, hasProjects := fields["projects"]
	return hasDefaults || hasProjects
}

func (inv *inventory) source(name string) (File, bool) {
	i, ok := inv.byName[name]
	if !ok {
		return File{}, false
	}
	return inv.files[i], true
}

func readInventory(root *os.Root) (*inventory, error) {
	inv := &inventory{byName: map[string]int{}, files: []File{}}
	for _, s := range standard {
		if err := inv.add(root, s, false); err != nil {
			return nil, err
		}
	}
	st, err := regular(root, definitionsDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		inv.problems = append(inv.problems, definitionsDir+": "+err.Error())
	case !st.IsDir():
		inv.problems = append(inv.problems, definitionsDir+" is not a directory")
	default:
		entries, err := fs.ReadDir(root.FS(), definitionsDir)
		if err != nil {
			return nil, err
		}
		if len(entries) > 2048 {
			return nil, errors.New("too many provider directory entries")
		}
		for _, e := range entries {
			ext := path.Ext(e.Name())
			if !e.IsDir() && (ext == ".yml" || ext == ".yaml") {
				if err := inv.add(root, source{path.Join(definitionsDir, e.Name()), def}, false); err != nil {
					return nil, err
				}
			}
		}
	}
	return inv, nil
}

func (inv *inventory) addReference(root *os.Root, name, kind string) error {
	if _, ok := inv.byName[name]; ok {
		return nil
	}
	// A configuration reference is not permission to display Git internals,
	// environment files or arbitrary application source.
	if !fs.ValidPath(name) || slices.Contains(strings.Split(name, "/"), ".git") {
		return nil
	}
	ext := strings.ToLower(path.Ext(name))
	if (kind == catalog && ext != ".json") || (kind == flow && ext != ".yml" && ext != ".yaml") {
		return nil
	}
	return inv.add(root, source{name, kind}, true)
}
