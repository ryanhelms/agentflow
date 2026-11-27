// Package acpregistry is agentflow's pinned copy of the official ACP
// registry (https://github.com/agentclientprotocol/registry, Apache-2.0) and
// the Message Translator that reads it (ADR
// docs/knowledge/engineering/decisions/event-driven-platform.md, section
// "Phase 6 pinned ACP registry copy").
//
// The files under v1/ are copied verbatim at one commit by
// scripts/refresh-acp-registry.sh and embedded. Nothing fetches the registry
// at runtime. Registry text is untrusted data: Load cleans and clamps display
// text, and drops any URL, package name or argument that fails the rules,
// with a Problem.
package acpregistry

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
)

//go:embed v1/agents/*.json v1/quarantine.json v1/agent.schema.json v1/LICENSE v1/SOURCE.json
var files embed.FS

// Agent is one registry entry, decoded with unknown fields refused, from
// agent.schema.json at the pinned commit.
type Agent struct {
	ID           string       `json:"id"` // ^[a-z][a-z0-9-]*$
	Name         string       `json:"name"`
	Version      string       `json:"version"` // X.Y.Z
	Description  string       `json:"description"`
	Repository   string       `json:"repository,omitempty"`
	Website      string       `json:"website,omitempty"`
	Authors      []string     `json:"authors,omitempty"`
	License      string       `json:"license,omitempty"`
	LicenseURL   string       `json:"license_url,omitempty"`
	Icon         string       `json:"icon,omitempty"` // decoded, never used
	Distribution Distribution `json:"distribution"`
	Preview      *Preview     `json:"preview,omitempty"` // decoded, never used: preview builds are unverified
}

type Distribution struct {
	Binary map[string]BinaryTarget `json:"binary,omitempty"` // keys: darwin-aarch64, darwin-x86_64, linux-aarch64, linux-aarch64-musl, linux-x86_64, linux-x86_64-musl, windows-aarch64, windows-x86_64
	NPX    *Package                `json:"npx,omitempty"`
	UVX    *Package                `json:"uvx,omitempty"`
}

type BinaryTarget struct {
	Archive string            `json:"archive"`          // https URL
	SHA256  string            `json:"sha256,omitempty"` // 64 hex
	Cmd     string            `json:"cmd"`              // e.g. "./goose"
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

type Package struct {
	Package string            `json:"package"` // name with optional version
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

type Preview struct {
	Version      string       `json:"version"`
	Distribution Distribution `json:"distribution"`
}

// Source is SOURCE.json: where and when the copy was taken.
type Source struct {
	Repository  string    `json:"repository"`
	Commit      string    `json:"commit"`
	CommittedAt time.Time `json:"committed_at"`
	Agents      int       `json:"agents"`
	Spec        struct {
		Repository string `json:"repository"`
		Tag        string `json:"tag"`
		Commit     string `json:"commit"`
	} `json:"spec"`
}

// Problem is a registry value that failed the rules and was dropped.
type Problem struct {
	ID      string // the agent
	Field   string // e.g. "distribution.npx.package"
	Message string // agentflow's own text; never the value
}

// Entry is one agent after the rules: display text cleaned and clamped,
// failing values dropped.
type Entry struct {
	Agent
	// Quarantined is set when quarantine.json lists the agent;
	// QuarantineReason is its reason, cleaned and clamped like a description.
	Quarantined      bool
	QuarantineReason string
	// EnvIgnored is set when a distribution sets env, which agentflow never
	// applies (note registry_env_ignored).
	EnvIgnored bool
}

// Catalog is the pinned copy.
type Catalog struct {
	Source   Source
	Entries  []Entry // sorted by ID
	Problems []Problem
}

// Get returns the entry with the ID.
func (c *Catalog) Get(id string) (Entry, bool) {
	i := sort.Search(len(c.Entries), func(i int) bool { return c.Entries[i].ID >= id })
	if i < len(c.Entries) && c.Entries[i].ID == id {
		return c.Entries[i], true
	}
	return Entry{}, false
}

// Display and value limits.
const (
	MaxNameLen        = 64
	MaxDescriptionLen = 256
	MaxArgs           = 16
)

var (
	idRule      = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	versionRule = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	sha256Rule  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	npxRule     = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*(@[0-9A-Za-z.+-]+)?$`)
	uvxRule     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(\[[A-Za-z0-9,._-]+\])?([=@<>~!]=?[0-9A-Za-z.*+-]+)?$`)
)

// Platforms are the binary distribution keys the schema allows.
var Platforms = []string{
	"darwin-aarch64", "darwin-x86_64", "linux-aarch64", "linux-aarch64-musl",
	"linux-x86_64", "linux-x86_64-musl", "windows-aarch64", "windows-x86_64",
}

var (
	loadOnce sync.Once
	loaded   *Catalog
	loadErr  error
)

// Load returns the embedded catalog, decoded once. A file that does not
// decode strictly is an error: TestPinnedRegistry keeps that from shipping.
func Load() (*Catalog, error) {
	loadOnce.Do(func() { loaded, loadErr = decode(files) })
	return loaded, loadErr
}

// FS returns the embedded files, for tests.
func FS() fs.FS { return files }

func decode(fsys fs.FS) (*Catalog, error) {
	c := &Catalog{}
	if err := decodeStrict(fsys, "v1/SOURCE.json", &c.Source); err != nil {
		return nil, err
	}
	var quarantine map[string]string
	if err := decodeStrict(fsys, "v1/quarantine.json", &quarantine); err != nil {
		return nil, err
	}
	names, err := fs.Glob(fsys, "v1/agents/*.json")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	for _, name := range names {
		var a Agent
		if err := decodeStrict(fsys, name, &a); err != nil {
			return nil, err
		}
		if want := strings.TrimSuffix(path.Base(name), ".json"); a.ID != want {
			return nil, fmt.Errorf("acpregistry: %s has the ID %q", name, a.ID)
		}
		e, problems := Check(a)
		if reason, ok := quarantine[a.ID]; ok {
			e.Quarantined = true
			e.QuarantineReason = harnessdef.CleanText(reason, MaxDescriptionLen)
		}
		c.Entries = append(c.Entries, e)
		c.Problems = append(c.Problems, problems...)
	}
	return c, nil
}

func decodeStrict(fsys fs.FS, name string, v any) error {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return fmt.Errorf("acpregistry: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("acpregistry: %s: %w", name, err)
	}
	if dec.More() {
		return fmt.Errorf("acpregistry: %s: more than one JSON value", name)
	}
	return nil
}

// Check applies the registry rules to a: display text is cleaned and
// clamped; a URL that is not https, a package name or argument that fails
// its rule, an unknown platform or a bad SHA-256 is dropped with a Problem.
// A binary target or package left without its archive or package is
// dropped whole.
func Check(a Agent) (Entry, []Problem) {
	var problems []Problem
	drop := func(field, msg string) { problems = append(problems, Problem{ID: a.ID, Field: field, Message: msg}) }
	if !idRule.MatchString(a.ID) || len(a.ID) > harnessdef.MaxIDLen {
		drop("id", "the ID must match ^[a-z][a-z0-9-]*$ and be at most 64 bytes")
	}
	a.Name = harnessdef.CleanText(a.Name, MaxNameLen)
	a.Description = harnessdef.CleanText(a.Description, MaxDescriptionLen)
	if !versionRule.MatchString(a.Version) {
		drop("version", "the version must be X.Y.Z")
		a.Version = ""
	}
	for _, u := range []struct {
		field string
		v     *string
	}{{"repository", &a.Repository}, {"website", &a.Website}, {"license_url", &a.LicenseURL}} {
		if *u.v != "" && !HTTPS(*u.v) {
			drop(u.field, "the URL must be https")
			*u.v = ""
		}
	}
	a.Icon, a.Preview = "", nil // never used
	authors := a.Authors[:0:0]
	for _, s := range a.Authors {
		if s = harnessdef.CleanText(s, MaxNameLen); s != "" {
			authors = append(authors, s)
		}
	}
	a.Authors = authors
	a.License = harnessdef.CleanText(a.License, MaxNameLen)

	e := Entry{}
	d := a.Distribution
	out := Distribution{}
	for _, key := range sortedKeys(d.Binary) {
		t := d.Binary[key]
		field := "distribution.binary." + key
		if !knownPlatform(key) {
			drop(field, "unknown platform")
			continue
		}
		if !HTTPS(t.Archive) {
			drop(field+".archive", "the archive URL must be https")
			continue
		}
		if t.SHA256 != "" && !sha256Rule.MatchString(t.SHA256) {
			drop(field+".sha256", "the SHA-256 must be 64 lowercase hex digits")
			t.SHA256 = ""
		}
		var ok bool
		if t.Args, ok = checkArgs(t.Args); !ok {
			drop(field+".args", "an argument breaks the arg rule, or there are more than 16")
		}
		if len(t.Env) > 0 {
			e.EnvIgnored = true
		}
		if out.Binary == nil {
			out.Binary = map[string]BinaryTarget{}
		}
		out.Binary[key] = t
	}
	for _, p := range []struct {
		name string
		in   *Package
		out  **Package
		rule *regexp.Regexp
	}{{"npx", d.NPX, &out.NPX, npxRule}, {"uvx", d.UVX, &out.UVX, uvxRule}} {
		if p.in == nil {
			continue
		}
		field := "distribution." + p.name
		if !p.rule.MatchString(p.in.Package) {
			drop(field+".package", "the package name breaks its rule")
			continue
		}
		pkg := *p.in
		var ok bool
		if pkg.Args, ok = checkArgs(pkg.Args); !ok {
			drop(field+".args", "an argument breaks the arg rule, or there are more than 16")
		}
		if len(pkg.Env) > 0 {
			e.EnvIgnored = true
		}
		*p.out = &pkg
	}
	a.Distribution = out
	e.Agent = a
	return e, problems
}

// checkArgs returns args, or nil and false when one fails the arg rule or
// there are too many.
func checkArgs(args []string) ([]string, bool) {
	if len(args) > MaxArgs {
		return nil, false
	}
	for _, s := range args {
		if !harnessdef.ValidArg(s) {
			return nil, false
		}
	}
	return args, true
}

// HTTPS reports whether u is an https URL with a host and no user
// information or whitespace.
func HTTPS(u string) bool {
	rest, ok := strings.CutPrefix(u, "https://")
	if !ok || rest == "" || strings.ContainsAny(u, " \t\r\n\"'<>\\`") {
		return false
	}
	host, _, _ := strings.Cut(rest, "/")
	return host != "" && !strings.Contains(host, "@")
}

func knownPlatform(key string) bool {
	for _, p := range Platforms {
		if p == key {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]BinaryTarget) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Platform is this machine's binary distribution key, or "" when the
// registry has none for it. musl says whether the C library is musl (Linux
// only).
func Platform(goos, goarch string, musl bool) string {
	arch := map[string]string{"arm64": "aarch64", "amd64": "x86_64"}[goarch]
	if arch == "" {
		return ""
	}
	switch goos {
	case "darwin", "windows":
		return goos + "-" + arch
	case "linux":
		if musl {
			return "linux-" + arch + "-musl"
		}
		return "linux-" + arch
	}
	return ""
}

// CurrentPlatform is Platform for this process.
func CurrentPlatform() string { return Platform(runtime.GOOS, runtime.GOARCH, isMusl()) }

// ErrNoCommand is returned by DefaultCommand when no distribution names one.
var ErrNoCommand = errors.New("acpregistry: no distribution names a command")

// DefaultCommand is the command name a definition defaults to: for a binary
// distribution for platform, the base name of its cmd; else for uvx the
// package name without version or extras; else for npx the last path
// segment of the package without its version. args are the platform's
// binary args, else the npx args, else the uvx args. The name is not
// checked against the command rule; the caller does that.
func DefaultCommand(d Distribution, platform string) (command string, args []string, err error) {
	switch {
	case d.Binary[platform].Cmd != "":
		t := d.Binary[platform]
		command, args = baseName(t.Cmd), t.Args
	case d.UVX != nil:
		command = uvxName(d.UVX.Package)
	case d.NPX != nil:
		command = npxName(d.NPX.Package)
	default:
		return "", nil, ErrNoCommand
	}
	if _, ok := d.Binary[platform]; !ok || d.Binary[platform].Cmd == "" {
		switch {
		case d.NPX != nil:
			args = d.NPX.Args
		case d.UVX != nil:
			args = d.UVX.Args
		}
	}
	return command, args, nil
}

// baseName is the last element of a cmd path, split at / and \, without
// ".exe".
func baseName(cmd string) string {
	if i := strings.LastIndexAny(cmd, `/\`); i >= 0 {
		cmd = cmd[i+1:]
	}
	if strings.HasSuffix(strings.ToLower(cmd), ".exe") {
		cmd = cmd[:len(cmd)-4]
	}
	return cmd
}

func npxName(pkg string) string {
	if strings.HasPrefix(pkg, "@") {
		if i := strings.Index(pkg, "/"); i >= 0 {
			pkg = pkg[i+1:]
		}
	}
	name, _, _ := strings.Cut(pkg, "@")
	return name
}

func uvxName(pkg string) string {
	if i := strings.IndexAny(pkg, "[=@<>~!"); i >= 0 {
		pkg = pkg[:i]
	}
	return pkg
}

// PackageVersion splits a package into its name and version ("" when it
// pins none): "@scope/name@1.2.3" or "name==1.2.3".
func PackageVersion(pkg string) (name, version string) {
	if i := strings.Index(pkg, "=="); i >= 0 {
		return pkg[:i], pkg[i+2:]
	}
	at := strings.LastIndex(pkg, "@")
	if at > 0 {
		return pkg[:at], pkg[at+1:]
	}
	return pkg, ""
}
