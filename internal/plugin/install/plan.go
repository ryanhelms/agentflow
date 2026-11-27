package install

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

// ErrRemoteSource is returned for a source that names something to download.
var ErrRemoteSource = errors.New("plugin add takes a local directory or YAML file, never an address")

var (
	schemeRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
	scpRe    = regexp.MustCompile(`^[^/\\\s]+@[^/\\\s]+:`)
)

// RefuseRemote returns an error wrapping ErrRemoteSource for a source that
// looks like an address: anything with "://", an scp-style Git address
// (git@host:owner/name), or a scheme such as file: or mailto:. A path that
// starts with ./, ../ or / is a path, and so is a Windows drive path (C:\x).
// A directory whose name holds a colon is added as ./name:with-colon.
func RefuseRemote(source string) error {
	address := false
	switch {
	case source == "":
		return errors.New("plugin add needs the path of a plugin directory or YAML file")
	case strings.Contains(source, "://"):
		address = true
	case strings.HasPrefix(source, "./"), strings.HasPrefix(source, "../"), strings.HasPrefix(source, "/"),
		strings.HasPrefix(source, `.\`), strings.HasPrefix(source, `..\`), strings.HasPrefix(source, `\`):
	case scpRe.MatchString(source):
		address = true
	case schemeRe.MatchString(source):
		// A drive letter is one letter: C:\plugins.
		address = strings.Index(source, ":") != 1
	}
	if address {
		return fmt.Errorf("%w: %q looks like an address; build or download the plugin first, then give its local path (start a relative path with ./)", ErrRemoteSource, source)
	}
	return nil
}

// AddPlan is what plugin add would install, worked out without writing
// anything. It holds the plugin's files as they were read, so Add writes
// exactly what the hash and the summary describe.
type AddPlan struct {
	Manifest manifest.Manifest
	// Hash is manifest.HashFiles(Files).
	Hash string
	// Files are the plugin's files. A single YAML file is one file named
	// manifest.FileName.
	Files []manifest.TreeFile
	// Source is the absolute path plugin add was given.
	Source string
	// Previous is the registry entry of the same plugin ID, if any.
	Previous *manifest.Installed
	// Outcome says what Add would do.
	Outcome Outcome
}

// Outcome is what plugin add did or would do.
type Outcome string

// The outcomes of plugin add.
const (
	// Installed: a plugin that was not installed.
	Installed Outcome = "installed"
	// Upgraded: another version of the plugin was installed. It is replaced.
	Upgraded Outcome = "upgraded"
	// Repaired: this exact version is registered, but its installed copy no
	// longer matches its hash. The copy is written again.
	Repaired Outcome = "repaired"
	// Unchanged: this exact version is installed and intact. Nothing is
	// written.
	Unchanged Outcome = "unchanged"
)

// Changes reports whether Add writes anything, and so whether the person
// must confirm.
func (o Outcome) Changes() bool { return o != Unchanged }

// PlanAdd reads the plugin at source, a local directory or a local YAML
// file, and checks it against the rules of ADR "Phase 5 install": regular
// files only, no symbolic link, at most manifest.MaxFiles files and
// manifest.MaxTreeBytes bytes, a manifest that validates, and for a process
// plugin an executable file at exec.command. It writes nothing.
func PlanAdd(home, source string) (*AddPlan, error) {
	if err := RefuseRemote(source); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(source)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, fmt.Errorf("plugin add: %w", err)
	}
	var files []manifest.TreeFile
	single := false
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return nil, &manifest.TreeError{Reason: "the path is a symbolic link: give the real directory or file"}
	case info.IsDir():
		if files, err = manifest.ReadTree(abs); err != nil {
			return nil, err
		}
	case info.Mode().IsRegular():
		single = true
		data, err := readManifestFile(abs, info)
		if err != nil {
			return nil, err
		}
		files = []manifest.TreeFile{{Path: manifest.FileName, Data: data}}
	default:
		return nil, &manifest.TreeError{Reason: "not a regular file or a directory"}
	}
	i := slices.IndexFunc(files, func(f manifest.TreeFile) bool { return f.Path == manifest.FileName })
	if i < 0 {
		return nil, fmt.Errorf("plugin add: %s has no %s", abs, manifest.FileName)
	}
	m, err := manifest.Load(files[i].Data)
	if err != nil {
		return nil, err
	}
	if single && m.Tier != manifest.TierDeclarative {
		return nil, problem(manifest.CodeTier, "tier", "a single YAML file can only be a declarative plugin: a process plugin is a directory that holds its executable")
	}
	if m.Tier == manifest.TierProcess {
		if err := checkExec(m, files); err != nil {
			return nil, err
		}
	}
	plan := &AddPlan{Manifest: m, Files: files, Hash: manifest.HashFiles(files), Source: abs, Outcome: Installed}
	reg, err := manifest.ReadRegistry(home)
	if err != nil {
		return nil, err
	}
	if j := slices.IndexFunc(reg.Plugins, func(p manifest.Installed) bool { return p.ID == m.ID }); j >= 0 {
		prev := reg.Plugins[j]
		plan.Previous = &prev
		switch {
		case prev.Hash != plan.Hash:
			plan.Outcome = Upgraded
		case intact(home, prev):
			plan.Outcome = Unchanged
		default:
			plan.Outcome = Repaired
		}
	} else if len(reg.Plugins) >= manifest.MaxInstalled {
		return nil, fmt.Errorf("plugin add: %d plugins are installed, the most there can be: remove one first", manifest.MaxInstalled)
	}
	return plan, nil
}

// intact reports whether the installed copy of p is where the registry says
// it is and still hashes to the registry's hash.
func intact(home string, p manifest.Installed) bool {
	dir := manifest.InstallPath(home, p.ID, p.Hash)
	if dir == "" {
		return false
	}
	got, err := manifest.HashDir(dir)
	return err == nil && got == p.Hash
}

func problem(code, at, msg string) error {
	return &manifest.ValidationError{Problems: []manifest.Problem{{Path: at, Code: code, Message: msg}}}
}

// readManifestFile reads a single manifest file, which must still be the
// file Lstat saw.
func readManifestFile(file string, before os.FileInfo) ([]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("plugin add: %w", err)
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, &manifest.TreeError{Path: filepath.Base(file), Reason: "changed while it was read"}
	}
	data, err := io.ReadAll(io.LimitReader(f, manifest.MaxManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("plugin add: %w", err)
	}
	if len(data) > manifest.MaxManifestBytes {
		return nil, problem(manifest.CodeLimit, "", fmt.Sprintf("the manifest is larger than %d KiB", manifest.MaxManifestBytes>>10))
	}
	return data, nil
}

// checkExec requires the process plugin's executable in the tree: the person
// builds it before they add the plugin, and agentflow never builds anything.
func checkExec(m manifest.Manifest, files []manifest.TreeFile) error {
	want := path.Clean(m.Exec.Command)
	names := []string{want}
	if runtime.GOOS == "windows" && path.Ext(want) == "" {
		names = append(names, want+".exe")
	}
	for _, name := range names {
		i := slices.IndexFunc(files, func(f manifest.TreeFile) bool { return f.Path == name })
		switch {
		case i < 0:
		case runtime.GOOS != "windows" && !files[i].Exec:
			return problem(manifest.CodeExec, "exec.command", fmt.Sprintf("%s is not executable: run chmod +x on it", m.Exec.Command))
		default:
			return nil
		}
	}
	return problem(manifest.CodeExec, "exec.command", fmt.Sprintf("%s is not in the plugin directory: build the plugin before you add it", m.Exec.Command))
}
