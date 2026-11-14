package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func tgz(t *testing.T, name string, body []byte) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "code-speed.png", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
	tw.Write([]byte("x"))
	tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write(body)
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestPlanFor(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "code-speed.app", "Contents", "MacOS")
	os.MkdirAll(app, 0o755)
	if p, err := PlanFor("darwin", "arm64", filepath.Join(app, "code-speed"), "v1.2.0"); err != nil ||
		p.Kind != MacApp || p.Asset != "code-speed-v1.2.0-macos-universal.zip" || p.Target != filepath.Join(dir, "code-speed.app") {
		t.Fatalf("mac %+v %v", p, err)
	}
	if _, err := PlanFor("darwin", "arm64", filepath.Join(dir, "code-speed"), "v1.2.0"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("bare mac binary: %v", err)
	}
	win := filepath.Join(dir, "win")
	os.MkdirAll(win, 0o755)
	if p, _ := PlanFor("windows", "amd64", filepath.Join(win, "code-speed.exe"), "v1.2.0"); p.Kind != WinPortable || p.Asset != "code-speed-v1.2.0-windows-amd64-portable.zip" {
		t.Fatalf("portable %+v", p)
	}
	os.WriteFile(filepath.Join(win, "uninstall.exe"), nil, 0o644)
	if p, _ := PlanFor("windows", "arm64", filepath.Join(win, "code-speed.exe"), "v1.2.0"); p.Kind != WinSetup || p.Asset != "code-speed-v1.2.0-windows-arm64-setup.exe" {
		t.Fatalf("setup %+v", p)
	}
	if _, err := PlanFor("windows", "amd64", `C:\Users\me\scoop\apps\code-speed\current\code-speed.exe`, "v1.2.0"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("scoop: %v", err)
	}
	if _, err := PlanFor("linux", "amd64", "/usr/bin/code-speed", "v1.2.0"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("deb: %v", err)
	}
}

func TestDownloadVerifiesAndSwaps(t *testing.T) {
	newBin := []byte("#!/bin/sh\necho new\n")
	pkg := tgz(t, "code-speed", newBin)
	sum := sha256.Sum256(pkg)
	asset := "code-speed-v9.0.0-linux-amd64.tar.gz"
	sums := hex.EncodeToString(sum[:]) + "  " + asset + "\n"
	// a test release key, and another one the app doesn't trust
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	oldKeys := PublicKeys
	defer func() { PublicKeys = oldKeys }()
	PublicKeys = []string{base64.StdEncoding.EncodeToString(pub)}
	sign := func(k ed25519.PrivateKey) string {
		return base64.StdEncoding.EncodeToString(ed25519.Sign(k, []byte(sums)))
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/SHA256SUMS":
			w.Write([]byte(sums))
		case "/SHA256SUMS.sig":
			w.Write([]byte(sign(priv) + "\n"))
		case "/other.sig":
			w.Write([]byte(sign(other)))
		case "/" + asset:
			w.Write(pkg)
		case "/bad":
			w.Write([]byte("tampered"))
		}
	}))
	defer srv.Close()
	oldClient, oldPrefix := Client, downloadPrefix
	defer func() { Client, downloadPrefix = oldClient, oldPrefix }()
	Client, downloadPrefix = srv.Client(), srv.URL+"/"

	dir := t.TempDir()
	exe := filepath.Join(dir, "code-speed")
	os.WriteFile(exe, []byte("old"), 0o755)
	p, err := PlanFor("linux", "amd64", exe, "v9.0.0")
	if err != nil {
		t.Fatal(err)
	}
	sumsA := Asset{Name: "SHA256SUMS", URL: srv.URL + "/SHA256SUMS"}
	sigA := Asset{Name: "SHA256SUMS.sig", URL: srv.URL + "/SHA256SUMS.sig"}
	pkgA := Asset{Name: asset, URL: srv.URL + "/" + asset}
	assets := []Asset{sumsA, sigA, pkgA}

	// a file that doesn't match its checksum is refused, nothing replaced
	bad := []Asset{sumsA, sigA, {Name: asset, URL: srv.URL + "/bad"}}
	if _, err := Download(context.Background(), p, bad); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("tampered: %v", err)
	}
	// checksums without a signature, or signed by a key the app doesn't
	// trust, aren't believed
	if _, err := Download(context.Background(), p, []Asset{sumsA, pkgA}); err == nil || !strings.Contains(err.Error(), "signed") {
		t.Fatalf("unsigned: %v", err)
	}
	forged := []Asset{sumsA, {Name: "SHA256SUMS.sig", URL: srv.URL + "/other.sig"}, pkgA}
	if _, err := Download(context.Background(), p, forged); err == nil || !strings.Contains(err.Error(), "release key") {
		t.Fatalf("other key: %v", err)
	}

	st, err := Download(context.Background(), p, assets)
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := st.Apply(12345)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); !bytes.Equal(b, newBin) {
		t.Fatalf("binary not swapped: %q", b)
	}
	if !strings.Contains(strings.Join(cmd.Args, " "), exe) {
		t.Fatalf("relaunch %v", cmd.Args)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("left behind: %v", ents)
	}
}

func TestMacAppSwap(t *testing.T) {
	if _, err := os.Stat("/usr/bin/ditto"); err != nil {
		t.Skip("macOS only")
	}
	dir := t.TempDir()
	mk := func(root, body string) string {
		app := filepath.Join(root, "code-speed.app")
		os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755)
		os.WriteFile(filepath.Join(app, "Contents", "MacOS", "code-speed"), []byte(body), 0o755)
		return app
	}
	installed := mk(filepath.Join(dir, "Applications"), "old")
	src := t.TempDir()
	mk(src, "new")
	zipPath := filepath.Join(t.TempDir(), "code-speed-v9.0.0-macos-universal.zip")
	if b, err := exec.Command("ditto", "-c", "-k", "--keepParent", filepath.Join(src, "code-speed.app"), zipPath).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	p, err := PlanFor("darwin", "arm64", filepath.Join(installed, "Contents", "MacOS", "code-speed"), "v9.0.0")
	if err != nil {
		t.Fatal(err)
	}
	stage, _ := os.MkdirTemp(filepath.Dir(installed), ".code-speed-update-")
	st := &Staged{Plan: p, File: zipPath, Dir: stage}
	cmd, err := st.Apply(1)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(installed, "Contents", "MacOS", "code-speed")); string(b) != "new" {
		t.Fatalf("app not swapped: %q", b)
	}
	if !strings.Contains(strings.Join(cmd.Args, " "), installed) {
		t.Fatalf("relaunch %v", cmd.Args)
	}
}
