package gitx

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Forge identifies a code host product (the "driver" that talks to it).
type Forge string

// Known forges. ForgeUnknown is the zero value.
const (
	ForgeUnknown     Forge = ""
	ForgeGitHub      Forge = "github"
	ForgeGitLab      Forge = "gitlab"
	ForgeBitbucket   Forge = "bitbucket"
	ForgeAzureDevOps Forge = "azuredevops"
)

// ParseForge turns a driver name from configuration into a Forge. It is
// case-insensitive and accepts a few common spellings.
func ParseForge(s string) (Forge, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "github":
		return ForgeGitHub, nil
	case "gitlab":
		return ForgeGitLab, nil
	case "bitbucket":
		return ForgeBitbucket, nil
	case "azuredevops", "azure-devops", "azure_devops", "azure", "ado":
		return ForgeAzureDevOps, nil
	}
	return ForgeUnknown, fmt.Errorf("unknown forge %q: use github, gitlab, bitbucket or azuredevops", s)
}

// Sentinel errors from ParseRemoteURL, for use with errors.Is.
var (
	// ErrLocalRemote means the remote is a filesystem path or file:// URL.
	ErrLocalRemote = errors.New("remote is a local path, not a hosted repository")
	// ErrInvalidRemote means the remote could not be parsed as a hosted repository.
	ErrInvalidRemote = errors.New("invalid remote URL")
)

// RemoteURL is a parsed git remote URL. It never carries credentials: the
// password and the user of http(s) URLs are dropped.
type RemoteURL struct {
	Transport string // https, http, ssh or git
	Host      string // lower case, without port
	Port      string // empty when not given
	User      string // ssh only, usually "git"
	Path      string // repository path without leading slash or .git, e.g. group/sub/repo
	Owner     string // Path without the last element (organization or namespace)
	Project   string // Azure DevOps project; empty elsewhere
	Name      string // repository name
}

func invalidRemote(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidRemote, reason)
}

// ParseRemoteURL parses an HTTPS, HTTP, ssh://, git:// or SCP-style
// (user@host:path) remote URL. Local paths and file:// URLs return an error
// wrapping ErrLocalRemote; anything else unusable wraps ErrInvalidRemote.
func ParseRemoteURL(raw string) (RemoteURL, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return RemoteURL{}, invalidRemote("empty")
	}
	if strings.ContainsAny(s, "\x00\r\n") {
		return RemoteURL{}, invalidRemote("contains control characters")
	}
	if strings.Contains(s, "://") {
		return parseURLForm(s)
	}
	return parseSCPForm(s)
}

func parseURLForm(s string) (RemoteURL, error) {
	u, err := url.Parse(s)
	if err != nil {
		return RemoteURL{}, invalidRemote("not a valid URL")
	}
	var out RemoteURL
	switch strings.ToLower(u.Scheme) {
	case "file":
		return RemoteURL{}, ErrLocalRemote
	case "https", "http", "git":
		out.Transport = strings.ToLower(u.Scheme)
	case "ssh", "git+ssh", "ssh+git":
		out.Transport = "ssh"
		out.User = u.User.Username()
	default:
		return RemoteURL{}, invalidRemote("unsupported scheme " + u.Scheme)
	}
	out.Host = strings.ToLower(u.Hostname())
	out.Port = u.Port()
	if out.Host == "" {
		return RemoteURL{}, invalidRemote("missing host")
	}
	if err := out.setPath(u.Path); err != nil {
		return RemoteURL{}, err
	}
	return out, nil
}

func parseSCPForm(s string) (RemoteURL, error) {
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, ".") || strings.HasPrefix(s, "~") {
		return RemoteURL{}, ErrLocalRemote
	}
	var user, host, path string
	rest := s
	if at := strings.LastIndex(strings.SplitN(s, ":", 2)[0], "@"); at >= 0 {
		user, rest = s[:at], s[at+1:]
	}
	if strings.HasPrefix(rest, "[") { // [ipv6]:path
		end := strings.Index(rest, "]:")
		if end < 0 {
			return RemoteURL{}, invalidRemote("malformed bracketed host")
		}
		host, path = rest[1:end], rest[end+2:]
	} else {
		var found bool
		host, path, found = strings.Cut(rest, ":")
		if !found {
			return RemoteURL{}, ErrLocalRemote
		}
	}
	if strings.Contains(host, "/") {
		return RemoteURL{}, ErrLocalRemote
	}
	if len(host) == 1 { // C:\repo or C:/repo
		return RemoteURL{}, ErrLocalRemote
	}
	if host == "" {
		return RemoteURL{}, invalidRemote("missing host")
	}
	out := RemoteURL{Transport: "ssh", Host: strings.ToLower(host), User: user}
	if err := out.setPath(path); err != nil {
		return RemoteURL{}, err
	}
	return out, nil
}

// setPath validates the repository path and fills Path, Owner, Project, Name.
func (u *RemoteURL) setPath(p string) error {
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	if p == "" {
		return invalidRemote("missing repository path")
	}
	segs := strings.Split(p, "/")
	for _, seg := range segs {
		if seg == "" || seg == "." || seg == ".." {
			return invalidRemote("malformed repository path")
		}
	}
	// Azure DevOps shapes, recognised by structure so they work for
	// dev.azure.com, *.visualstudio.com and mapped on-premises hosts alike:
	//   ssh:    v3/<org>/<project>/<repo>
	//   https:  [<org>/][DefaultCollection/]<project>/_git/<repo>
	if len(segs) == 4 && segs[0] == "v3" && u.Transport == "ssh" && isAzureHost(u.Host) {
		u.Path, u.Owner, u.Project, u.Name = strings.Join(segs[1:], "/"), segs[1], segs[2], segs[3]
		return nil
	}
	if i := indexOf(segs, "_git"); i >= 0 {
		if i == len(segs)-1 {
			return invalidRemote("missing repository name after _git")
		}
		name := segs[i+1]
		var before []string
		for _, seg := range segs[:i] {
			if !strings.EqualFold(seg, "DefaultCollection") {
				before = append(before, seg)
			}
		}
		vs := strings.HasSuffix(u.Host, ".visualstudio.com")
		var org, project string
		if vs {
			// The organization is the subdomain; the path starts at the project.
			org = strings.TrimSuffix(u.Host, ".visualstudio.com")
			project = name // repository-only form: the project is the repo
			if len(before) > 0 {
				project = before[len(before)-1]
			}
		} else {
			project = name
			if len(before) > 0 {
				org = before[0]
			}
			if len(before) > 1 {
				project = before[len(before)-1]
			}
		}
		u.Owner, u.Project, u.Name = org, project, name
		u.Path = strings.Join(nonEmpty(org, project, name), "/")
		return nil
	}
	if len(segs) < 2 {
		return invalidRemote("expected owner/name")
	}
	u.Path = p
	u.Owner = strings.Join(segs[:len(segs)-1], "/")
	u.Name = segs[len(segs)-1]
	return nil
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func nonEmpty(s ...string) []string {
	var out []string
	for _, x := range s {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

func isAzureHost(host string) bool {
	return host == "dev.azure.com" || host == "ssh.dev.azure.com" ||
		host == "vs-ssh.visualstudio.com" || strings.HasSuffix(host, ".visualstudio.com")
}

// InferForge guesses the forge from well-known public hostnames only. It never
// probes the network; self-hosted hosts return ForgeUnknown and need an
// explicit mapping (see DetectOptions.Hosts).
func InferForge(host string) Forge {
	h := strings.ToLower(host)
	switch {
	case h == "github.com", h == "www.github.com", h == "ssh.github.com", strings.HasSuffix(h, ".ghe.com"):
		return ForgeGitHub
	case h == "gitlab.com", h == "www.gitlab.com", h == "altssh.gitlab.com":
		return ForgeGitLab
	case h == "bitbucket.org", h == "www.bitbucket.org":
		return ForgeBitbucket
	case isAzureHost(h):
		return ForgeAzureDevOps
	}
	return ForgeUnknown
}

// canonicalHost folds alternative SSH endpoints of the same service onto one
// name so a fork's HTTPS and SSH remotes compare equal.
func canonicalHost(host string) string {
	switch host {
	case "ssh.github.com", "www.github.com":
		return "github.com"
	case "ssh.dev.azure.com", "vs-ssh.visualstudio.com":
		return "dev.azure.com"
	case "altssh.gitlab.com", "www.gitlab.com":
		return "gitlab.com"
	case "www.bitbucket.org":
		return "bitbucket.org"
	}
	if isAzureHost(host) {
		return "dev.azure.com"
	}
	return host
}

// Remote is one configured git remote after parsing and forge resolution.
type Remote struct {
	Name  string
	URL   RemoteURL
	Forge Forge
	// Mapped is true when Forge came from an explicit host mapping rather than
	// from a well-known public hostname.
	Mapped bool
}

// ID identifies the repository independent of transport: host and path,
// lower-cased. Two remotes with the same ID point at the same repository.
func (r Remote) ID() string {
	return canonicalHost(r.URL.Host) + "/" + strings.ToLower(r.URL.Path)
}

// SkippedRemote is a remote that could not be used, with the reason.
type SkippedRemote struct {
	Name   string
	Local  bool // a filesystem path rather than a malformed URL
	Reason string
}

// Status is the outcome class of forge detection.
type Status string

// Detection statuses.
const (
	// StatusOK: Source, Target and Forge are set.
	StatusOK Status = "ok"
	// StatusLocalOnly: no remote points at a hosted repository.
	StatusLocalOnly Status = "local-only"
	// StatusUnknown: remotes exist but the forge cannot be determined.
	StatusUnknown Status = "unknown"
	// StatusAmbiguous: more than one reading is possible; configure roles.
	StatusAmbiguous Status = "ambiguous"
)

// DetectOptions is explicit configuration for detection. The zero value is valid.
type DetectOptions struct {
	// Hosts maps a hostname (no scheme or port, case-insensitive) to a driver
	// name accepted by ParseForge. It takes precedence over inference and is
	// the only way to identify self-hosted installations.
	Hosts map[string]string
	// Source names the remote changes are pushed to (the fork when forking).
	// Default: "origin", or the only usable remote.
	Source string
	// Target names the remote changes are proposed to (where the change/PR
	// lands). Default: "upstream" when it is a different repository than
	// Source, otherwise Source.
	Target string
}

// Detection is the result of detecting the code host for a repository.
type Detection struct {
	Status Status
	Forge  Forge // forge of Target when Status is StatusOK
	Source *Remote
	Target *Remote
	// Fork is true when Source and Target are different repositories.
	Fork bool
	// Remotes are all usable remotes, sorted by name.
	Remotes []Remote
	// Skipped are remotes that could not be parsed, sorted by name.
	Skipped []SkippedRemote
	// Message explains a non-OK status and what to do about it. It is empty
	// for StatusOK.
	Message string
}

// OK reports whether detection succeeded.
func (d Detection) OK() bool { return d.Status == StatusOK }

// Remotes returns the fetch URL of every configured remote, keyed by name.
// URLs are resolved by git, so url.<base>.insteadOf rewrites apply. It never
// contacts the remote.
func (r *Repo) Remotes(ctx context.Context) (map[string]string, error) {
	out, err := r.Git(ctx, "remote")
	if err != nil {
		return nil, err
	}
	remotes := map[string]string{}
	for _, name := range strings.Split(out, "\n") {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		u, err := r.Git(ctx, "remote", "get-url", name)
		if err != nil {
			return nil, err
		}
		remotes[name] = strings.TrimSpace(u)
	}
	return remotes, nil
}

// DetectForge reads the repository's remotes with git (no network) and
// resolves the forge, the source (fork) and the target. The error is non-nil
// only for invalid options or when git itself fails; "no hosted remote" and
// similar outcomes are reported in Detection.Status.
func (r *Repo) DetectForge(ctx context.Context, opts DetectOptions) (Detection, error) {
	remotes, err := r.Remotes(ctx)
	if err != nil {
		return Detection{}, err
	}
	return DetectForge(remotes, opts)
}

// DetectForge resolves the forge and fork roles from remote name -> URL.
// The result is deterministic: remotes are always considered in name order.
func DetectForge(remotes map[string]string, opts DetectOptions) (Detection, error) {
	hosts, err := normalizeHosts(opts.Hosts)
	if err != nil {
		return Detection{}, err
	}

	names := make([]string, 0, len(remotes))
	for n := range remotes {
		names = append(names, n)
	}
	sort.Strings(names)

	var d Detection
	byName := map[string]*Remote{}
	skipped := map[string]SkippedRemote{}
	for _, n := range names {
		u, err := ParseRemoteURL(remotes[n])
		if err != nil {
			s := SkippedRemote{Name: n, Local: errors.Is(err, ErrLocalRemote), Reason: err.Error()}
			d.Skipped = append(d.Skipped, s)
			skipped[n] = s
			continue
		}
		rm := Remote{Name: n, URL: u, Forge: InferForge(u.Host)}
		if f, ok := hosts[u.Host]; ok {
			rm.Forge, rm.Mapped = f, true
		}
		d.Remotes = append(d.Remotes, rm)
	}
	for i := range d.Remotes {
		byName[d.Remotes[i].Name] = &d.Remotes[i]
	}

	// usable resolves a remote name to a parsed remote, or explains why not.
	usable := func(role, name string) (*Remote, string) {
		if rm, ok := byName[name]; ok {
			return rm, ""
		}
		if s, ok := skipped[name]; ok {
			return nil, fmt.Sprintf("%s remote %q cannot be used: %s", role, name, s.Reason)
		}
		return nil, fmt.Sprintf("%s remote %q does not exist; remotes: %s", role, name, listNames(names))
	}
	fail := func(status Status, format string, a ...any) (Detection, error) {
		d.Status, d.Message = status, fmt.Sprintf(format, a...)
		return d, nil
	}

	if len(d.Remotes) == 0 {
		if len(names) == 0 {
			return fail(StatusLocalOnly, "no git remotes are configured; add one with `git remote add origin <url>` to use a code host")
		}
		for _, s := range d.Skipped {
			if !s.Local {
				return fail(StatusUnknown, "no remote could be parsed; fix its URL (e.g. https://host/owner/repo.git or git@host:owner/repo.git): %s", skippedSummary(d.Skipped))
			}
		}
		return fail(StatusLocalOnly, "every remote is a local path, so there is no code host: %s", skippedSummary(d.Skipped))
	}

	// Source.
	var src *Remote
	switch {
	case opts.Source != "":
		var why string
		if src, why = usable("source", opts.Source); src == nil {
			return fail(StatusUnknown, "%s", why)
		}
	case byName["origin"] != nil:
		src = byName["origin"]
	default:
		if s, ok := skipped["origin"]; ok {
			return fail(StatusUnknown, "origin cannot be used: %s; fix its URL or choose the source remote explicitly (one of: %s)", s.Reason, listRemotes(d.Remotes))
		}
		if len(d.Remotes) > 1 {
			return fail(StatusAmbiguous, "there is no origin and several remotes (%s); name the source remote (where you push) explicitly", listRemotes(d.Remotes))
		}
		src = &d.Remotes[0]
	}

	// Target.
	tgt := src
	switch {
	case opts.Target != "":
		var why string
		if tgt, why = usable("target", opts.Target); tgt == nil {
			return fail(StatusUnknown, "%s", why)
		}
	default:
		if up, ok := byName["upstream"]; ok {
			if up.ID() != src.ID() {
				tgt = up
			}
		} else if s, ok := skipped["upstream"]; ok {
			d.Source = src
			return fail(StatusAmbiguous, "upstream cannot be used: %s; fix its URL, or name the target remote explicitly if %s is the target", s.Reason, src.Name)
		}
	}
	d.Source, d.Target = src, tgt
	d.Fork = src.ID() != tgt.ID()

	for _, rm := range []*Remote{src, tgt} {
		if rm.Forge == ForgeUnknown {
			return fail(StatusUnknown, "host %q (remote %s) is not a well-known code host; map it to a driver (github, gitlab, bitbucket or azuredevops) in the host mapping", rm.URL.Host, rm.Name)
		}
	}
	if src.Forge != tgt.Forge || canonicalHost(src.URL.Host) != canonicalHost(tgt.URL.Host) {
		return fail(StatusAmbiguous, "source %s is %s but target %s is %s; changes cannot cross code hosts. Pick remotes on the same host, or fix the host mapping", src.Name, src.Forge, tgt.Name, tgt.Forge)
	}
	d.Status, d.Forge = StatusOK, tgt.Forge
	return d, nil
}

func normalizeHosts(in map[string]string) (map[string]Forge, error) {
	out := make(map[string]Forge, len(in))
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic error and duplicate handling
	for _, k := range keys {
		h := strings.ToLower(strings.TrimSpace(k))
		if h == "" || strings.ContainsAny(h, "/: @\t") {
			return nil, fmt.Errorf("host mapping key %q must be a bare hostname without scheme, port or path", k)
		}
		f, err := ParseForge(in[k])
		if err != nil {
			return nil, fmt.Errorf("host mapping for %q: %w", k, err)
		}
		if prev, dup := out[h]; dup && prev != f {
			return nil, fmt.Errorf("host mapping for %q conflicts with another entry that differs only by case", k)
		}
		out[h] = f
	}
	return out, nil
}

func listNames(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

func listRemotes(rs []Remote) string {
	names := make([]string, len(rs))
	for i, r := range rs {
		names[i] = r.Name
	}
	return strings.Join(names, ", ")
}

func skippedSummary(ss []SkippedRemote) string {
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = s.Name + ": " + s.Reason
	}
	return strings.Join(parts, "; ")
}
