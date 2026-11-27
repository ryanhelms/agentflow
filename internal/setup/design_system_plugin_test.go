package setup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const (
	designPlugin    = "plugins/design-system"
	designReference = designPlugin + "/skills/agentflow-design/reference"
	designVendor    = "website/vendor"
	designRebuild   = "run scripts/build-design-system-plugin.sh"
)

// designPackages maps a reference directory to the tarball name prefix of its package.
var designPackages = map[string]string{
	"tokens":     "agentflow-design-tokens-",
	"components": "agentflow-design-components-",
	"patterns":   "agentflow-design-patterns-",
}

// marketplaceClients lists every marketplace file with the manifest its client reads
// from a plugin's source directory.
var marketplaceClients = []struct{ client, marketplace, manifest string }{
	{"claude", ".claude-plugin/marketplace.json", ".claude-plugin/plugin.json"},
	{"codex", ".agents/plugins/marketplace.json", ".codex-plugin/plugin.json"},
	{"cursor", ".cursor-plugin/marketplace.json", ".cursor-plugin/plugin.json"},
	{"copilot", ".github/plugin/marketplace.json", "plugin.json"},
}

func repoFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func repoDir(t *testing.T, name string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join("..", "..", filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// designTarball returns the name of the one vendored tarball of a design package.
func designTarball(t *testing.T, pkg string) string {
	t.Helper()
	var found []string
	for _, name := range repoDir(t, designVendor) {
		if strings.HasPrefix(name, designPackages[pkg]) && strings.HasSuffix(name, ".tgz") {
			found = append(found, name)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s: want one %s*.tgz, found %v", designVendor, designPackages[pkg], found)
	}
	return found[0]
}

// tarballFiles returns the regular files of a .tgz by their path in the archive.
func tarballFiles(t *testing.T, name string) map[string][]byte {
	t.Helper()
	zipped, err := gzip.NewReader(bytes.NewReader(repoFile(t, name)))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	files := map[string][]byte{}
	archive := tar.NewReader(zipped)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return files
		}
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		data, err := io.ReadAll(archive)
		if err != nil {
			t.Fatalf("%s: %s: %v", name, header.Name, err)
		}
		files[path.Clean(header.Name)] = data
	}
}

// pluginSource reads a marketplace entry's source directory: a path, or an object
// with a path (Codex).
func pluginSource(raw json.RawMessage) (string, bool) {
	var dir string
	if json.Unmarshal(raw, &dir) == nil {
		return dir, true
	}
	var local struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(raw, &local) != nil || local.Path == "" {
		return "", false
	}
	return local.Path, true
}

func TestMarketplacesListBothPlugins(t *testing.T) {
	for _, tc := range marketplaceClients {
		t.Run(tc.client, func(t *testing.T) {
			var marketplace struct {
				Name    string `json:"name"`
				Plugins []struct {
					Name   string          `json:"name"`
					Source json.RawMessage `json:"source"`
				} `json:"plugins"`
			}
			if err := json.Unmarshal(repoFile(t, tc.marketplace), &marketplace); err != nil {
				t.Fatalf("%s: %v", tc.marketplace, err)
			}
			if marketplace.Name != "agentflow" {
				t.Errorf("%s: marketplace name %q, want agentflow", tc.marketplace, marketplace.Name)
			}
			listed := map[string]bool{}
			for _, plugin := range marketplace.Plugins {
				dir, ok := pluginSource(plugin.Source)
				if !ok || path.IsAbs(dir) || strings.HasPrefix(path.Clean(dir), "..") {
					t.Errorf("%s: plugin %q needs a source directory inside the repository", tc.marketplace, plugin.Name)
					continue
				}
				manifestPath := path.Join(dir, tc.manifest)
				var manifest struct {
					Name string `json:"name"`
				}
				data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(manifestPath)))
				if err != nil {
					t.Errorf("%s: plugin %q has no %s manifest: %v", tc.marketplace, plugin.Name, tc.client, err)
					continue
				}
				if err := json.Unmarshal(data, &manifest); err != nil {
					t.Errorf("%s: %v", manifestPath, err)
					continue
				}
				if manifest.Name != plugin.Name {
					t.Errorf("%s: name %q, but %s lists it as %q", manifestPath, manifest.Name, tc.marketplace, plugin.Name)
					continue
				}
				listed[plugin.Name] = true
			}
			for _, want := range []string{"agentflow", "design-system"} {
				if !listed[want] {
					t.Errorf("%s does not list a usable %q plugin", tc.marketplace, want)
				}
			}
		})
	}
}

func TestDesignSystemPluginCarriesVendoredPackages(t *testing.T) {
	var want []string
	for pkg := range designPackages {
		want = append(want, designTarball(t, pkg))
	}
	sort.Strings(want)
	got := repoDir(t, designPlugin+"/vendor")
	sort.Strings(got)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("%s/vendor has %v, want %v: %s", designPlugin, got, want, designRebuild)
	}
	skill := string(repoFile(t, designPlugin+"/skills/agentflow-design/SKILL.md"))
	for _, name := range want {
		if !bytes.Equal(repoFile(t, designPlugin+"/vendor/"+name), repoFile(t, designVendor+"/"+name)) {
			t.Errorf("%s/vendor/%s differs from %s: %s", designPlugin, name, designVendor, designRebuild)
		}
		if !strings.Contains(skill, name) {
			t.Errorf("SKILL.md does not name %s in its install command", name)
		}
	}
}

func TestDesignSystemReferenceMatchesPackages(t *testing.T) {
	for pkg := range designPackages {
		t.Run(pkg, func(t *testing.T) {
			packed := tarballFiles(t, designVendor+"/"+designTarball(t, pkg))
			root := filepath.Join("..", "..", filepath.FromSlash(designReference), pkg)
			copied := map[string]bool{}
			err := filepath.WalkDir(root, func(file string, entry fs.DirEntry, err error) error {
				if err != nil || entry.IsDir() {
					return err
				}
				rel, err := filepath.Rel(root, file)
				if err != nil {
					return err
				}
				name := "package/" + filepath.ToSlash(rel)
				copied[name] = true
				data, err := os.ReadFile(file)
				if err != nil {
					return err
				}
				source, ok := packed[name]
				switch {
				case !ok:
					t.Errorf("reference/%s/%s is not in the package: %s", pkg, filepath.ToSlash(rel), designRebuild)
				case !bytes.Equal(data, source):
					t.Errorf("reference/%s/%s differs from the package: %s", pkg, filepath.ToSlash(rel), designRebuild)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(copied) == 0 {
				t.Fatalf("reference/%s is empty: %s", pkg, designRebuild)
			}
			// Every type declaration and the tokens are part of the reference.
			for name := range packed {
				if (strings.HasSuffix(name, ".d.ts") || name == "package/tokens.css") && !copied[name] {
					t.Errorf("reference/%s lacks %s: %s", pkg, strings.TrimPrefix(name, "package/"), designRebuild)
				}
			}
		})
	}
}

func TestDesignSystemManifestsUseComponentsVersion(t *testing.T) {
	var pkg struct {
		Version string `json:"version"`
	}
	packed := tarballFiles(t, designVendor+"/"+designTarball(t, "components"))
	if err := json.Unmarshal(packed["package/package.json"], &pkg); err != nil || pkg.Version == "" {
		t.Fatalf("components package.json has no version: %v", err)
	}
	for _, tc := range marketplaceClients {
		manifestPath := designPlugin + "/" + tc.manifest
		var manifest struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if err := json.Unmarshal(repoFile(t, manifestPath), &manifest); err != nil {
			t.Fatalf("%s: %v", manifestPath, err)
		}
		if manifest.Name != "design-system" {
			t.Errorf("%s: name %q, want design-system", manifestPath, manifest.Name)
		}
		if manifest.Version != pkg.Version {
			t.Errorf("%s: version %q, want the components version %q: %s", manifestPath, manifest.Version, pkg.Version, designRebuild)
		}
	}
}

// The design packages are not published, so the website must install every one of
// them from its own tarball: a registry copy under the same name is someone else's.
func TestWebsiteInstallsDesignPackagesFromTarballs(t *testing.T) {
	var lock struct {
		Packages map[string]struct {
			Resolved string `json:"resolved"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(repoFile(t, "website/package-lock.json"), &lock); err != nil {
		t.Fatal(err)
	}
	found := 0
	for name, entry := range lock.Packages {
		if !strings.Contains(name, "node_modules/@agentflow/") {
			continue
		}
		found++
		if !strings.HasPrefix(entry.Resolved, "file:vendor/agentflow-design-") {
			t.Errorf("%s resolves to %q, want a tarball in website/vendor", name, entry.Resolved)
		}
	}
	if found != len(designPackages) {
		t.Errorf("website/package-lock.json has %d design packages, want %d", found, len(designPackages))
	}
}
