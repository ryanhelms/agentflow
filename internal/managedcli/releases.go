package managedcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nscaledev/agentflow/internal/provision"
	"github.com/nscaledev/agentflow/internal/provision/user"
	"github.com/nscaledev/agentflow/internal/update"
)

type pinnedRelease struct {
	version    provision.Version
	source     string
	binarySHA  string
	archiveSHA string
	executable string
}

// Sources and SHA-256 pins come from the vendors' versioned releases. For gh,
// the executable digest is derived from the separately verified archive.
var releases = map[string]map[string]pinnedRelease{
	"gh": {
		"darwin/amd64":  ghRelease("macOS_amd64.zip", "gh", "23602cbccbbdc6285a8520745b714cb927ce2ca621779373c2a20cd8e33fda66", "a23748af178ecfe35e76d6d36247489ce4f110fb8893ca3281e7f2dc0bd5a4a2"),
		"darwin/arm64":  ghRelease("macOS_arm64.zip", "gh", "2a5d03103ae444927383b93d9ffd65579a26e2eb900ce1cf11f55258d5319ded", "31dc9ba8d1a68a6ae01e7cec4aea39579948d81693ff7010c27ecd83b22d9fd4"),
		"linux/amd64":   ghRelease("linux_amd64.tar.gz", "gh", "79e7ab5db738c48777b696e374f8b428f0647debfb424f44d2f426370407fe2f", "fa7cff06d4db9eee98e00b10ac93beebfe1822109ac04a6cbbf9d806c21d0073"),
		"linux/arm64":   ghRelease("linux_arm64.tar.gz", "gh", "b1278c75107423d818f1937fd92cafc9e5e9b606631568f68d7fb816f5885a02", "2ef7512bb4d097c62d98bd248465fee66e7488f213dd49af345ad5af10d15d20"),
		"windows/amd64": ghRelease("windows_amd64.zip", "gh.exe", "8813a72c899900afaa10e3fec8fbc64b908c89dbfe837e95ed2fa500fc140627", "77437defc10c5cf35ca0d25d626d58a5210de8ad10bf270c02df4630754bcd69"),
		"windows/arm64": ghRelease("windows_arm64.zip", "gh.exe", "5282a09c7757210c6e2dfb11e14e646df9b3b6a49f155465f5f881c3b7", "d644b2d2a588177a5f71a4cf9706e0014c6939c08ff4b5d8fde808c66e1cd9f2"),
	},
	"twg": {
		"darwin/arm64":  twgRelease("darwin-arm64", "e95b9285a97f3d79a4ae1122264a43f72522740944de2dc8ff1b92376418926d"),
		"darwin/amd64":  twgRelease("darwin-x64", "81dfa7629841778d35879e83970c148022878fb51f9bea7d31605d35ab1f0cfb"),
		"linux/arm64":   twgRelease("linux-arm64", "9acea0127e9bc12f8687da81434fe61e4578ae7dde556dde626d3ecaab45350c"),
		"linux/amd64":   twgRelease("linux-x64", "289e804ffae54fe958d0be3ea8374dc806a7e341d3cf34e5feca81ad0c1f41bf"),
		"windows/arm64": twgRelease("windows-arm64", "9ed726f0b0507db86a43c24df3f777b812d52efda32e3ddcf5ca62a4a6d1f777"),
		"windows/amd64": twgRelease("windows-x64", "410a00113c6487d08f320c4d6dc57cf4426f8cb75397dc99df6fa81a3220ee92"),
	},
}

func ghRelease(platform, executable, archiveSHA, binarySHA string) pinnedRelease {
	return pinnedRelease{
		version: provision.Version{Major: 2, Minor: 105}, source: "https://github.com/cli/cli/releases/download/v2.105.0/gh_2.105.0_" + platform,
		archiveSHA: archiveSHA, binarySHA: binarySHA, executable: executable,
	}
}

func twgRelease(platform, digest string) pinnedRelease {
	source := "https://teamwork-graph.atlassian.com/cli/twg-" + platform + "-v1.4.6"
	if strings.HasPrefix(platform, "windows-") {
		source += ".exe"
	}
	return pinnedRelease{version: provision.Version{Major: 1, Minor: 4, Patch: 6}, source: source, binarySHA: digest}
}

func (r pinnedRelease) open(ctx context.Context, artifact provision.Artifact) (io.ReadCloser, error) {
	if r.archiveSHA == "" {
		return user.OpenArtifact(ctx, artifact)
	}
	stage, err := os.MkdirTemp("", "agentflow-cli-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if stage != "" {
			_ = os.RemoveAll(stage)
		}
	}()
	name := filepath.Join(stage, filepath.Base(r.source))
	if err := downloadArchive(ctx, r.source, name); err != nil {
		return nil, err
	}
	if err := update.VerifyDigest(name, r.archiveSHA); err != nil {
		return nil, err
	}
	if err := update.ExtractBinary(name, stage, r.executable); err != nil {
		return nil, err
	}
	// Keep the staging directory through Close, including on Windows.
	file, err := os.Open(filepath.Join(stage, r.executable))
	if err != nil {
		return nil, err
	}
	stage = ""
	return &stagedBinary{File: file}, nil
}

type stagedBinary struct{ *os.File }

func (f *stagedBinary) Close() error {
	return errors.Join(f.File.Close(), os.RemoveAll(filepath.Dir(f.Name())))
}

func downloadArchive(ctx context.Context, source, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	client := http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 4 || req.URL.Scheme != "https" || req.URL.Host != "release-assets.githubusercontent.com" {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("CLI release download returned HTTP %d", resp.StatusCode)
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	const limit = 100 << 20
	n, copyErr := io.Copy(file, io.LimitReader(resp.Body, limit+1))
	if n > limit {
		copyErr = errors.New("CLI release archive exceeds the download size limit")
	}
	return errors.Join(copyErr, file.Close())
}
