// Package update checks GitHub Releases for a newer client and replaces the CLI binary after
// verifying the archive against the release's SHA256SUMS.
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
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Repo is where releases are published.
const Repo = "stevennight/nyatunnel-client"

// Release is the latest stable release.
type Release struct {
	Version string `json:"version"` // without "v"
	URL     string `json:"url"`     // release page
	assets  map[string]string
}

// AssetURL returns the download URL of a named asset.
func (r *Release) AssetURL(name string) (string, bool) {
	u, ok := r.assets[name]
	return u, ok
}

var client = &http.Client{Timeout: 5 * time.Minute}

// APIBase can be replaced in tests.
var APIBase = "https://api.github.com"

// Latest fetches the latest stable release.
func Latest(ctx context.Context) (*Release, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, APIBase+"/repos/"+Repo+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var body struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	r := &Release{Version: strings.TrimPrefix(body.TagName, "v"), URL: body.HTMLURL, assets: map[string]string{}}
	for _, a := range body.Assets {
		r.assets[a.Name] = a.URL
	}
	return r, nil
}

// Newer reports whether version a is newer than b (MAJOR.MINOR.PATCH, suffixes ignored).
func Newer(a, b string) bool {
	pa, pb := parse(a), parse(b)
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parse(v string) [3]int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	v, _, _ = strings.Cut(v, "-")
	var out [3]int
	for i, p := range strings.SplitN(v, ".", 3) {
		n, err := strconv.Atoi(p)
		if err != nil {
			return [3]int{}
		}
		out[i] = n
	}
	return out
}

// CLIAsset is the archive name of this platform's CLI for a version.
func CLIAsset(version string) string {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("NyaTunnel-CLI_%s_%s_%s%s", version, runtime.GOOS, arch, ext)
}

func download(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", filepath.Base(url), resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if int64(len(b)) > limit {
		return nil, errors.New("download too large")
	}
	return b, err
}

// expectedSum finds name in a SHA256SUMS file.
func expectedSum(sums []byte, name string) (string, bool) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			return strings.ToLower(fields[0]), true
		}
	}
	return "", false
}

// extract returns the nyatunnel binary from a release archive.
func extract(name string, archive []byte) ([]byte, error) {
	want := "nyatunnel"
	if strings.HasSuffix(name, ".zip") {
		want += ".exe"
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if filepath.Base(f.Name) == want {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(io.LimitReader(rc, 200<<20))
			}
		}
		return nil, errors.New("binary not found in archive")
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			return nil, errors.New("binary not found in archive")
		}
		if filepath.Base(h.Name) == want && h.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, 200<<20))
		}
	}
}

// fetchVerified downloads a release asset and checks it against the release's SHA256SUMS.
func fetchVerified(ctx context.Context, r *Release, name string, limit int64) ([]byte, error) {
	assetURL, ok := r.AssetURL(name)
	if !ok {
		return nil, fmt.Errorf("release %s has no %s", r.Version, name)
	}
	sumsURL, ok := r.AssetURL("SHA256SUMS")
	if !ok {
		return nil, errors.New("release has no SHA256SUMS")
	}
	sums, err := download(ctx, sumsURL, 1<<20)
	if err != nil {
		return nil, err
	}
	want, ok := expectedSum(sums, name)
	if !ok {
		return nil, fmt.Errorf("SHA256SUMS does not list %s", name)
	}
	data, err := download(ctx, assetURL, limit)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != want {
		return nil, errors.New("checksum mismatch: refusing to install")
	}
	return data, nil
}

// InstallCLI downloads, verifies and installs the CLI of release r over exe.
func InstallCLI(ctx context.Context, r *Release, exe string) error {
	name := CLIAsset(r.Version)
	archive, err := fetchVerified(ctx, r, name, 200<<20)
	if err != nil {
		return err
	}
	bin, err := extract(name, archive)
	if err != nil {
		return err
	}
	return replace(exe, bin)
}

// Installer kinds of the desktop app.
const (
	KindNSIS     = "nsis"     // Windows: run passively, it replaces the installed app and restarts it
	KindDMG      = "dmg"      // macOS: opened for the user
	KindAppImage = "appimage" // Linux AppImage: replaces the running file
	KindDeb      = "deb"      // Linux package: opened with the system installer
)

// GUIAsset is this platform's desktop installer for a version. appImage tells whether the app
// runs from an AppImage (Linux).
func GUIAsset(version string, appImage bool) (name, kind string, ok bool) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64":
		return fmt.Sprintf("NyaTunnel_%s_windows_x64.exe", version), KindNSIS, true
	case "darwin/arm64":
		return fmt.Sprintf("NyaTunnel_%s_macos_arm64.dmg", version), KindDMG, true
	case "darwin/amd64":
		return fmt.Sprintf("NyaTunnel_%s_macos_x64.dmg", version), KindDMG, true
	case "linux/amd64":
		if appImage {
			return fmt.Sprintf("NyaTunnel_%s_linux_x64.AppImage", version), KindAppImage, true
		}
		return fmt.Sprintf("NyaTunnel_%s_linux_x64.deb", version), KindDeb, true
	}
	return "", "", false
}

// UpdateDir is where downloaded installers are kept; the desktop app only runs files from here.
func UpdateDir() string { return filepath.Join(os.TempDir(), "NyaTunnel-update") }

// DownloadGUI fetches and verifies this platform's installer of release r into UpdateDir and
// returns its path and kind.
func DownloadGUI(ctx context.Context, r *Release, appImage bool) (string, string, error) {
	name, kind, ok := GUIAsset(r.Version, appImage)
	if !ok {
		return "", "", fmt.Errorf("no desktop installer for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	dir := UpdateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	path := filepath.Join(dir, name)
	data, err := fetchVerified(ctx, r, name, 300<<20)
	if err != nil {
		return "", "", err
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0o700); err != nil {
		return "", "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", "", err
	}
	return path, kind, nil
}

// replace swaps the executable. A running Windows executable cannot be overwritten but can be
// renamed, so the old one is moved aside first.
func replace(exe string, bin []byte) error {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".nyatunnel-update-*")
	if err != nil {
		return fmt.Errorf("cannot write next to %s: %w", exe, err)
	}
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	_ = os.Chmod(tmp.Name(), 0o755)
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), exe); err != nil {
		_ = os.Rename(old, exe)
		return err
	}
	if runtime.GOOS != "windows" {
		_ = os.Remove(old)
	}
	return nil
}
