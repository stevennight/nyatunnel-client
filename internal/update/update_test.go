package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func archiveFor(t *testing.T, name string, bin []byte) []byte {
	var buf bytes.Buffer
	dir := strings.TrimSuffix(strings.TrimSuffix(name, ".zip"), ".tar.gz")
	if strings.HasSuffix(name, ".zip") {
		zw := zip.NewWriter(&buf)
		w, _ := zw.Create(dir + "/nyatunnel.exe")
		w.Write(bin)
		zw.Close()
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: dir + "/nyatunnel", Mode: 0o755, Size: int64(len(bin)), Typeflag: tar.TypeReg})
	tw.Write(bin)
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestInstallCLI(t *testing.T) {
	name := CLIAsset("9.9.9")
	archive := archiveFor(t, name, []byte("new binary"))
	sum := sha256.Sum256(archive)
	good := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name)
	sums := good
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/" + Repo + "/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"v9.9.9","html_url":"https://example/r","assets":[{"name":%q,"browser_download_url":%q},{"name":"SHA256SUMS","browser_download_url":%q}]}`,
				name, "http://"+r.Host+"/a", "http://"+r.Host+"/sums")
		case "/a":
			w.Write(archive)
		case "/sums":
			w.Write([]byte(sums))
		}
	}))
	defer srv.Close()
	APIBase = srv.URL
	defer func() { APIBase = "https://api.github.com" }()

	r, err := Latest(context.Background())
	if err != nil || r.Version != "9.9.9" || !Newer(r.Version, "0.1.0") || Newer("0.1.0-dev", "0.1.0") {
		t.Fatalf("latest: %+v %v", r, err)
	}
	exe := filepath.Join(t.TempDir(), "nyatunnel")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	os.WriteFile(exe, []byte("old binary"), 0o755)

	sums = strings.Repeat("0", 64) + "  " + name + "\n"
	if err := InstallCLI(context.Background(), r, exe); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("tampered archive installed: %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old binary" {
		t.Fatal("binary replaced despite checksum mismatch")
	}
	sums = good
	if err := InstallCLI(context.Background(), r, exe); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new binary" {
		t.Fatalf("binary not replaced: %q", b)
	}
}

func TestDownloadGUI(t *testing.T) {
	name, kind, ok := GUIAsset("9.9.9", false)
	if !ok {
		t.Skip("no desktop installer for this platform")
	}
	payload := []byte("installer bytes")
	sum := sha256.Sum256(payload)
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/i":
			w.Write(payload)
		case "/sums":
			w.Write([]byte(sums))
		}
	}))
	defer srv.Close()
	r := &Release{Version: "9.9.9", assets: map[string]string{name: srv.URL + "/i", "SHA256SUMS": srv.URL + "/sums"}}
	path, gotKind, err := DownloadGUI(context.Background(), r, false)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	if gotKind != kind || filepath.Dir(path) != UpdateDir() || filepath.Base(path) != name {
		t.Fatalf("got %s %s", path, gotKind)
	}
	if b, _ := os.ReadFile(path); string(b) != string(payload) {
		t.Fatal("wrong content")
	}
	sums = strings.Repeat("1", 64) + "  " + name + "\n"
	if _, _, err := DownloadGUI(context.Background(), r, false); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("tampered installer accepted: %v", err)
	}
}
