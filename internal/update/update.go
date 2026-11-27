// Package update keeps agentflow on the latest verified release.
//
// Installs live in ${XDG_CACHE_HOME:-~/.cache}/agentflow/<version>/agentflow with a
// "current" symlink, the same layout the repo launcher uses. A new release is
// downloaded to a staging dir, verified (SHA-256 from checksums.txt and the
// GitHub build attestation via `gh attestation verify`), extracted, and then
// activated by an atomic symlink swap.
package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/gh"
)

// Repo is where releases and the version policy live.
const Repo = "ryanhelms/agentflow"

// Options configures the updater from repo and user config.
type Options struct {
	Channel    string // stable | beta
	Track      *int   // major version to follow; nil = current major
	Pin        string
	CheckEvery time.Duration
	Disabled   bool // AGENTFLOW_NO_UPDATE or update.auto: false
}

// Updater checks for and installs releases.
type Updater struct {
	Current string
	Opts    Options
	GH      *gh.Client
	Dir     string // cache root
	Log     func(format string, args ...any)
}

// New returns an updater for the running version.
func New(current string, opts Options, client *gh.Client) (*Updater, error) {
	dir, err := CacheDir()
	if err != nil {
		return nil, err
	}
	if os.Getenv("AGENTFLOW_NO_UPDATE") == "1" {
		opts.Disabled = true
	}
	if opts.CheckEvery == 0 {
		opts.CheckEvery = 6 * time.Hour
	}
	return &Updater{Current: current, Opts: opts, GH: client, Dir: dir, Log: func(string, ...any) {}}, nil
}

// CacheDir returns the install cache root, creating it: %LOCALAPPDATA%\agentflow
// on Windows, otherwise ${XDG_CACHE_HOME:-~/.cache}/agentflow. The repo launchers
// use the same paths and the same check (see CacheRoot).
func CacheDir() (string, error) {
	dir, err := CacheRoot()
	if err != nil {
		return "", err
	}
	return dir, os.MkdirAll(dir, 0o755)
}

// CacheRoot returns the install cache root without creating it. A repository's
// environment can set XDG_CACHE_HOME, HOME or LOCALAPPDATA, so a root inside a
// git working tree or git directory is never used: the account's own folder
// (~/.cache/agentflow from the user database, or the Windows known local
// application data folder) is used instead, with a warning. See config.UserDir.
func CacheRoot() (string, error) {
	return cacheRoot(runtime.GOOS, os.Getenv, ownCacheRoot)
}

// cacheRoot keeps the variables' raw text (no filepath.Join, which would hide a
// ".." from the check), as the launchers do.
func cacheRoot(goos string, getenv func(string) string, own func(goos string) (string, error)) (string, error) {
	sep := string(filepath.Separator)
	ownFn := func() (string, error) { return own(goos) }
	if goos == "windows" {
		dir := ""
		if la := getenv("LOCALAPPDATA"); la != "" {
			dir = la + sep + "agentflow"
		}
		return config.UserDir(dir, "LOCALAPPDATA", "install cache", ownFn)
	}
	if base := getenv("XDG_CACHE_HOME"); base != "" {
		return config.UserDir(base+sep+"agentflow", "XDG_CACHE_HOME", "install cache", ownFn)
	}
	dir := ""
	if home := getenv("HOME"); home != "" {
		dir = home + sep + ".cache" + sep + "agentflow"
	}
	return config.UserDir(dir, "HOME", "install cache", ownFn)
}

// ownCacheRoot is the account's own cache root.
func ownCacheRoot(goos string) (string, error) {
	if goos == "windows" {
		local, err := localAppData()
		if err != nil {
			return "", err
		}
		return filepath.Join(local, "agentflow"), nil
	}
	home, err := config.AccountHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", "agentflow"), nil
}

// ---- versions ----

// Version is a parsed semantic version.
type Version struct {
	Major, Minor, Patch int
	Pre                 string
}

// ParseVersion parses "v1.2.3" or "1.2.3-beta.1".
func ParseVersion(s string) (Version, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	core, pre, _ := strings.Cut(s, "-")
	core, _, _ = strings.Cut(core, "+")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, false
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil {
			return Version{}, false
		}
		n[i] = v
	}
	return Version{n[0], n[1], n[2], pre}, true
}

// Less reports v < o (pre-releases sort before their release).
func (v Version) Less(o Version) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	if v.Patch != o.Patch {
		return v.Patch < o.Patch
	}
	if v.Pre == o.Pre {
		return false
	}
	if v.Pre == "" {
		return false
	}
	if o.Pre == "" {
		return true
	}
	return v.Pre < o.Pre
}

func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// ---- releases ----

// Release is a GitHub release.
type Release struct {
	Tag        string  `json:"tag_name"`
	Prerelease bool    `json:"prerelease"`
	Draft      bool    `json:"draft"`
	HTMLURL    string  `json:"html_url"`
	Assets     []Asset `json:"assets"`
	version    Version
}

// Asset is a release file.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Version returns the release version.
func (r Release) Version() Version { return r.version }

// Latest returns the newest release for the channel and track, or nil.
func (u *Updater) Latest(ctx context.Context) (*Release, error) {
	var rels []Release
	if err := u.GH.Do(ctx, "GET", "/repos/"+Repo+"/releases?per_page=50", nil, &rels); err != nil {
		return nil, err
	}
	track := u.Opts.Track
	if track == nil {
		if cur, ok := ParseVersion(u.Current); ok {
			track = &cur.Major
		}
	}
	var best *Release
	for i := range rels {
		r := rels[i]
		if r.Draft || (r.Prerelease && u.Opts.Channel != "beta") {
			continue
		}
		v, ok := ParseVersion(r.Tag)
		if !ok || (track != nil && v.Major != *track) {
			continue
		}
		if u.Opts.Pin != "" && v.String() != strings.TrimPrefix(u.Opts.Pin, "v") {
			continue
		}
		r.version = v
		if best == nil || best.version.Less(v) {
			best = &r
		}
	}
	return best, nil
}

// LatestStable resolves the release GitHub labels Latest, the same endpoint
// used by the bootstrap launchers. An explicit repo pin or track still applies.
func (u *Updater) LatestStable(ctx context.Context) (*Release, error) {
	var r Release
	if err := u.GH.Do(ctx, "GET", "/repos/"+Repo+"/releases/latest", nil, &r); err != nil {
		return nil, err
	}
	v, ok := ParseVersion(r.Tag)
	if !ok || r.Draft || r.Prerelease || v.Pre != "" || r.Tag != "v"+v.String() {
		return nil, fmt.Errorf("unexpected latest stable release tag %q", r.Tag)
	}
	if u.Opts.Pin != "" && strings.TrimPrefix(u.Opts.Pin, "v") != v.String() {
		return nil, fmt.Errorf("latest release %s conflicts with update.pin %s; review the project setting", r.Tag, u.Opts.Pin)
	}
	if u.Opts.Track != nil && *u.Opts.Track != v.Major {
		return nil, fmt.Errorf("latest release %s conflicts with update.track %d; review the project setting", r.Tag, *u.Opts.Track)
	}
	policy, err := u.FetchPolicy(ctx)
	if err != nil {
		return nil, fmt.Errorf("verify latest release policy: %w", err)
	}
	if policy == nil {
		return nil, errors.New("latest release policy is unavailable")
	}
	if policy.Requires(v.String()) {
		return nil, fmt.Errorf("latest release %s is blocked by version policy", r.Tag)
	}
	r.version = v
	return &r, nil
}

// NewerThanCurrent reports whether r is newer than the running build.
func (u *Updater) NewerThanCurrent(r *Release) bool {
	cur, ok := ParseVersion(u.Current)
	if !ok {
		return false // dev builds never self-update
	}
	return cur.Less(r.version)
}

// ---- policy ----

// Policy is policy/versions.json.
type Policy struct {
	MinVersion string   `json:"min_version"`
	Blocked    []string `json:"blocked"`
	Message    string   `json:"message"`
}

// Requires reports whether version v must not run.
func (p *Policy) Requires(v string) bool {
	cur, ok := ParseVersion(v)
	if !ok {
		return false
	}
	if slices.Contains(p.Blocked, cur.String()) || slices.Contains(p.Blocked, "v"+cur.String()) {
		return true
	}
	if min, ok := ParseVersion(p.MinVersion); ok && cur.Less(min) {
		return true
	}
	return false
}

// FetchPolicy reads the version policy with an ETag cache. Offline returns the cached copy or nil.
func (u *Updater) FetchPolicy(ctx context.Context) (*Policy, error) {
	cache := filepath.Join(u.Dir, "policy.json")
	etagFile := cache + ".etag"
	etag, _ := os.ReadFile(etagFile)
	b, newTag, err := u.GH.File(ctx, Repo, "main", "policy/versions.json", strings.TrimSpace(string(etag)))
	switch {
	case errors.Is(err, gh.ErrNotModified):
		b, err = os.ReadFile(cache)
	case err == nil:
		_ = os.WriteFile(cache, b, 0o644)
		_ = os.WriteFile(etagFile, []byte(newTag), 0o644)
	default:
		if old, rerr := os.ReadFile(cache); rerr == nil {
			b, err = old, nil
		}
	}
	if err != nil || len(b) == 0 {
		return nil, err
	}
	var p Policy
	return &p, json.Unmarshal(b, &p)
}

// ErrRestartRequired means a forced update was installed; re-run the command.
var ErrRestartRequired = errors.New("agentflow was updated because this version is blocked; restart the command or session")

// Enforce blocks only if this build must not run. It installs the latest
// release when the policy requires it.
func (u *Updater) Enforce(ctx context.Context) error {
	pol, err := u.FetchPolicy(ctx)
	if err != nil || pol == nil || !pol.Requires(u.Current) {
		return nil
	}
	rel, err := u.Latest(ctx)
	if err == nil && rel == nil {
		err = errors.New("no eligible release found")
	}
	if err == nil {
		err = u.Install(ctx, rel)
	}
	if err != nil {
		return fmt.Errorf("agentflow %s is blocked (%s) and the update failed: %w", u.Current, pol.Message, err)
	}
	return ErrRestartRequired
}

// ---- background check ----

func (u *Updater) stampPath() string { return filepath.Join(u.Dir, "last-check") }

// Due reports whether a background check should run.
func (u *Updater) Due() bool {
	if u.Opts.Disabled || u.Opts.Pin != "" {
		return false
	}
	if _, ok := ParseVersion(u.Current); !ok {
		return false
	}
	st, err := os.Stat(u.stampPath())
	return err != nil || time.Since(st.ModTime()) >= u.Opts.CheckEvery
}

// Touch records a check now (used before spawning a detached updater).
func (u *Updater) Touch() { u.touch() }

// SetNotice stores a one-time message shown by the next agentflow run.
func (u *Updater) SetNotice(msg string) {
	_ = os.WriteFile(filepath.Join(u.Dir, "notice"), []byte(msg), 0o644)
}

// TakeNotice returns and clears the pending notice.
func (u *Updater) TakeNotice() string {
	p := filepath.Join(u.Dir, "notice")
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	_ = os.Remove(p)
	return strings.TrimSpace(string(b))
}

func (u *Updater) touch() {
	_ = os.WriteFile(u.stampPath(), []byte(time.Now().UTC().Format(time.RFC3339)), 0o644)
}

// Background checks and installs in a goroutine if due. notify is called with
// the installed release; it never blocks the caller.
func (u *Updater) Background(ctx context.Context, notify func(*Release)) {
	if !u.Due() {
		return
	}
	go func() {
		defer u.touch()
		ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		rel, err := u.Latest(ctx)
		if err != nil || rel == nil || !u.NewerThanCurrent(rel) || u.Installed(rel.version.String()) {
			return
		}
		if err := u.Install(ctx, rel); err != nil {
			u.Log("agentflow: update to %s failed, staying on %s: %v", rel.Tag, u.Current, err)
			return
		}
		if notify != nil {
			notify(rel)
		}
	}()
}

// Installed reports whether version is already in the cache and current.
func (u *Updater) Installed(version string) bool {
	return u.currentVersion() == version
}

// currentVersion returns the active version from the "current" symlink
// (or current.txt on Windows, which holds the version folder path).
func (u *Updater) currentVersion() string {
	if runtime.GOOS == "windows" {
		b, err := os.ReadFile(filepath.Join(u.Dir, "current.txt"))
		if err != nil {
			return ""
		}
		return filepath.Base(strings.TrimSpace(string(b)))
	}
	target, err := os.Readlink(filepath.Join(u.Dir, "current"))
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

// ---- install ----

// AssetName is the archive name GoReleaser produces for this platform.
func AssetName(version string) string {
	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("agentflow_%s_%s_%s.%s", version, runtime.GOOS, runtime.GOARCH, ext)
}

// Install downloads, verifies and activates a release.
func (u *Updater) Install(ctx context.Context, rel *Release) error {
	unlock, err := u.lock()
	if err != nil {
		return err
	}
	defer unlock()
	ver := rel.version.String()
	if ver == "0.0.0" {
		ver = strings.TrimPrefix(rel.Tag, "v")
	}
	name := AssetName(ver)
	var archive, sums *Asset
	for i := range rel.Assets {
		switch rel.Assets[i].Name {
		case name:
			archive = &rel.Assets[i]
		case "checksums.txt":
			sums = &rel.Assets[i]
		}
	}
	if archive == nil || sums == nil {
		return fmt.Errorf("release %s has no %s or checksums.txt", rel.Tag, name)
	}
	stage, err := os.MkdirTemp(u.Dir, ".staging-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	archivePath := filepath.Join(stage, name)
	if err := u.download(ctx, archive.URL, archivePath); err != nil {
		return err
	}
	sumsPath := filepath.Join(stage, "checksums.txt")
	if err := u.download(ctx, sums.URL, sumsPath); err != nil {
		return err
	}
	if err := VerifyChecksum(archivePath, sumsPath); err != nil {
		return err
	}
	if err := VerifyAttestation(ctx, archivePath); err != nil {
		return err
	}
	dest := filepath.Join(u.Dir, ver)
	tmpDest := dest + ".new"
	_ = os.RemoveAll(tmpDest)
	if err := extract(archivePath, tmpDest); err != nil {
		return err
	}
	_ = os.RemoveAll(dest)
	if err := os.Rename(tmpDest, dest); err != nil {
		return err
	}
	if err := u.activate(ver); err != nil {
		return err
	}
	return u.prune(2)
}

func (u *Updater) download(ctx context.Context, assetURL, dest string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", assetURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/octet-stream")
	if u.GH.Token != "" {
		req.Header.Set("Authorization", "Bearer "+u.GH.Token)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", assetURL, resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// VerifyChecksum checks file against its line in a GoReleaser checksums.txt.
func VerifyChecksum(file, sumsFile string) error {
	sums, err := os.ReadFile(sumsFile)
	if err != nil {
		return err
	}
	want := ""
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && f[1] == filepath.Base(file) {
			want = f[0]
		}
	}
	if want == "" {
		return fmt.Errorf("%s not listed in checksums.txt", filepath.Base(file))
	}
	return VerifyDigest(file, want)
}

// VerifyDigest compares the file with a pinned SHA-256 digest.
func VerifyDigest(file, want string) error {
	fh, err := os.Open(file)
	if err != nil {
		return err
	}
	defer fh.Close()
	h := sha256.New()
	if _, err := io.Copy(h, fh); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("checksum mismatch for %s", filepath.Base(file))
	}
	return nil
}

// SignerWorkflow is the only workflow allowed to have built a release agentflow installs.
const SignerWorkflow = Repo + "/.github/workflows/release.yml"

// VerifyAttestation runs `gh attestation verify` against the agentflow repo, requiring the
// provenance to come from agentflow's release workflow (not any workflow in the repo).
func VerifyAttestation(ctx context.Context, file string) error {
	if os.Getenv("AGENTFLOW_INSECURE_SKIP_ATTESTATION") == "1" {
		return nil // tests and air-gapped mirrors only
	}
	// GitHub has no attestations for private repositories owned by a user, so a private
	// release is installed on its checksum alone. A failed lookup still verifies.
	if privateRepo(ctx) {
		fmt.Fprintln(os.Stderr, "agentflow: private release, skipping build attestation (checksum verified)")
		return nil
	}
	cmd := exec.CommandContext(ctx, "gh", "attestation", "verify", file, "--repo", Repo, "--signer-workflow", SignerWorkflow)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("attestation verification failed (needs a recent gh CLI): %s", strings.TrimSpace(out.String()))
	}
	return nil
}

// privateRepo reports whether gh can see that the agentflow repo is private.
var privateRepo = func(ctx context.Context) bool {
	out, err := exec.CommandContext(ctx, "gh", "repo", "view", Repo, "--json", "visibility", "-q", ".visibility").Output()
	return err == nil && strings.TrimSpace(string(out)) == "PRIVATE"
}

func extract(archive, dest string) error {
	bin := "agentflow"
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	return ExtractBinary(archive, dest, bin)
}

// ExtractBinary copies only the named regular executable from a verified archive.
func ExtractBinary(archive, dest, bin string) error {
	if filepath.Base(bin) != bin || bin == "." || bin == ".." {
		return errors.New("expected a single executable filename")
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	if strings.HasSuffix(archive, ".zip") {
		zr, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer zr.Close()
		for _, f := range zr.File {
			if filepath.Base(f.Name) != bin || !f.Mode().IsRegular() {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return err
			}
			defer rc.Close()
			return writeExec(filepath.Join(dest, bin), rc)
		}
		return fmt.Errorf("%s not found in archive", bin)
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("%s not found in archive", bin)
		}
		if err != nil {
			return err
		}
		if filepath.Base(h.Name) == bin && h.Typeflag == tar.TypeReg {
			return writeExec(filepath.Join(dest, bin), tr)
		}
	}
}

func writeExec(path string, r io.Reader) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755) //nolint:gosec // the installed binary must be executable
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, io.LimitReader(r, 200<<20)); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func (u *Updater) activate(version string) error {
	prev := u.currentVersion()
	if runtime.GOOS == "windows" {
		tmp := filepath.Join(u.Dir, "current.txt.tmp")
		if err := os.WriteFile(tmp, []byte(filepath.Join(u.Dir, version)), 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, filepath.Join(u.Dir, "current.txt")); err != nil {
			return err
		}
		if prev != "" && prev != version {
			_ = os.WriteFile(filepath.Join(u.Dir, "previous"), []byte(prev), 0o644)
		}
		return nil
	}
	link := filepath.Join(u.Dir, "current")
	tmp := link + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(version, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		return err
	}
	if prev != "" && prev != version {
		_ = os.WriteFile(filepath.Join(u.Dir, "previous"), []byte(prev), 0o644)
	}
	return nil
}

// Rollback switches current back to the previously active version.
func (u *Updater) Rollback() (string, error) {
	b, err := os.ReadFile(filepath.Join(u.Dir, "previous"))
	if err != nil {
		return "", errors.New("no previous version recorded")
	}
	prev := strings.TrimSpace(string(b))
	if _, err := os.Stat(filepath.Join(u.Dir, prev)); err != nil {
		return "", fmt.Errorf("previous version %s is no longer installed", prev)
	}
	return prev, u.activate(prev)
}

// prune keeps the current and previous versions and removes older ones.
func (u *Updater) prune(keep int) error {
	entries, err := os.ReadDir(u.Dir)
	if err != nil {
		return err
	}
	prev, _ := os.ReadFile(filepath.Join(u.Dir, "previous"))
	keepSet := map[string]bool{u.currentVersion(): true, strings.TrimSpace(string(prev)): true}
	var versions []Version
	for _, e := range entries {
		if v, ok := ParseVersion(e.Name()); ok && e.IsDir() {
			versions = append(versions, v)
		}
	}
	slices.SortFunc(versions, func(a, b Version) int {
		if b.Less(a) {
			return -1
		}
		if a.Less(b) {
			return 1
		}
		return 0
	})
	for i, v := range versions {
		if i < keep || keepSet[v.String()] {
			continue
		}
		_ = os.RemoveAll(filepath.Join(u.Dir, v.String()))
	}
	return nil
}

func (u *Updater) lock() (func(), error) {
	p := filepath.Join(u.Dir, ".lock")
	for i := 0; i < 50; i++ {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(p) }, nil
		}
		if st, serr := os.Stat(p); serr == nil && time.Since(st.ModTime()) > 10*time.Minute {
			_ = os.Remove(p) // stale lock from a crashed process
			continue
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil, errors.New("another agentflow process is installing an update")
}
