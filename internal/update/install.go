package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Installing a release in place: the package for this system is
// downloaded from the release, checked against the release's SHA256SUMS,
// put where the running copy is, and started once this one has quit.
//
//	macOS    the .app (from …-macos-universal.zip) is swapped for the new one
//	Windows  installed: the setup runs silently into the same folder;
//	         portable: code-speed.exe is swapped (the old one is renamed)
//	Linux    a binary from the .tar.gz is swapped, where its folder is
//	         writable; the .deb and .rpm are left to the package manager
//
// SHA256SUMS counts only with a valid SHA256SUMS.sig (signed.go).

// Asset is one file of a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Size int64  `json:"size"`
}

// Kinds of install.
const (
	MacApp      = "mac-app"
	WinSetup    = "win-setup"
	WinPortable = "win-portable"
	LinuxBinary = "linux-binary"
)

// Plan is how this copy would be updated.
type Plan struct {
	Kind   string `json:"kind"`
	Asset  string `json:"asset"`  // the release file to fetch
	Target string `json:"target"` // what is replaced: the .app or the binary
}

// ErrUnsupported: this copy can't update itself (a package manager's, a
// read-only folder); the release page has the download.
var ErrUnsupported = errors.New("this copy can't update itself")

// PlanFor works out how the copy at exe updates to version.
func PlanFor(goos, goarch, exe, version string) (Plan, error) {
	dir := filepath.Dir(exe)
	switch goos {
	case "darwin":
		app := filepath.Dir(filepath.Dir(dir)) // …/code-speed.app/Contents/MacOS/code-speed
		if !strings.HasSuffix(app, ".app") || filepath.Base(dir) != "MacOS" {
			return Plan{}, fmt.Errorf("%w: not inside an app bundle", ErrUnsupported)
		}
		if !writable(filepath.Dir(app)) {
			return Plan{}, fmt.Errorf("%w: %s is read-only", ErrUnsupported, filepath.Dir(app))
		}
		return Plan{MacApp, "code-speed-" + version + "-macos-universal.zip", app}, nil
	case "windows":
		if strings.Contains(strings.ToLower(exe), `\scoop\apps\`) {
			return Plan{}, fmt.Errorf("%w: installed by Scoop (scoop update code-speed)", ErrUnsupported)
		}
		if _, err := os.Stat(filepath.Join(dir, "uninstall.exe")); err == nil {
			return Plan{WinSetup, "code-speed-" + version + "-windows-" + goarch + "-setup.exe", exe}, nil
		}
		if !writable(dir) {
			return Plan{}, fmt.Errorf("%w: %s is read-only", ErrUnsupported, dir)
		}
		return Plan{WinPortable, "code-speed-" + version + "-windows-" + goarch + "-portable.zip", exe}, nil
	case "linux":
		if strings.HasPrefix(exe, "/usr/") || !writable(dir) {
			return Plan{}, fmt.Errorf("%w: installed by a package manager", ErrUnsupported)
		}
		return Plan{LinuxBinary, "code-speed-" + version + "-linux-" + goarch + ".tar.gz", exe}, nil
	}
	return Plan{}, ErrUnsupported
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".code-speed-w-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// Downloads may come from github.com and its asset hosts only.
var Client = &http.Client{
	Timeout: 10 * time.Minute,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		h := req.URL.Hostname()
		if len(via) > 5 || req.URL.Scheme != "https" || !(h == "github.com" || strings.HasSuffix(h, ".githubusercontent.com")) {
			return errors.New("redirect refused")
		}
		return nil
	},
}

const maxAsset = 300 << 20

// downloadPrefix is where release files are; tests point it elsewhere.
var downloadPrefix = "https://github.com/"

func download(ctx context.Context, u, path string) error {
	if !strings.HasPrefix(u, downloadPrefix) {
		return fmt.Errorf("unexpected download URL %q", u)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "code-speed")
	resp, err := Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download answered %d", resp.StatusCode)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxAsset+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxAsset {
		err = errors.New("download too large")
	}
	return err
}

// sums reads a SHA256SUMS file: "<hex>  <name>" lines.
func sums(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.Fields(sc.Text())
		if len(parts) == 2 && len(parts[0]) == 64 {
			out[strings.TrimPrefix(parts[1], "*")] = strings.ToLower(parts[0])
		}
	}
	return out, sc.Err()
}

func fileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Staged is a verified update ready to apply.
type Staged struct {
	Plan Plan
	File string // the downloaded asset
	Dir  string // the staging folder, removed after
}

// Download fetches and verifies the plan's asset from the release.
func Download(ctx context.Context, p Plan, assets []Asset) (*Staged, error) {
	find := func(name string) *Asset {
		for i := range assets {
			if assets[i].Name == name {
				return &assets[i]
			}
		}
		return nil
	}
	a, sumsAsset, sigAsset := find(p.Asset), find("SHA256SUMS"), find("SHA256SUMS.sig")
	if a == nil {
		return nil, fmt.Errorf("the release has no %s", p.Asset)
	}
	if sumsAsset == nil || sigAsset == nil {
		return nil, errors.New("the release has no signed SHA256SUMS to check it against")
	}
	// stage next to the target, so the swap is a rename on one volume
	dir, err := os.MkdirTemp(filepath.Dir(p.Target), ".code-speed-update-")
	if err != nil {
		if dir, err = os.MkdirTemp("", "code-speed-update-"); err != nil {
			return nil, err
		}
	}
	fail := func(err error) (*Staged, error) {
		os.RemoveAll(dir)
		return nil, err
	}
	sp := filepath.Join(dir, "SHA256SUMS")
	if err := download(ctx, sumsAsset.URL, sp); err != nil {
		return fail(err)
	}
	if err := download(ctx, sigAsset.URL, sp+".sig"); err != nil {
		return fail(err)
	}
	// the checksums count only once the release key vouches for them
	sb, err := os.ReadFile(sp)
	if err != nil {
		return fail(err)
	}
	sig, err := os.ReadFile(sp + ".sig")
	if err != nil {
		return fail(err)
	}
	if err := VerifySums(sb, sig); err != nil {
		return fail(err)
	}
	want, err := sums(sp)
	if err != nil {
		return fail(err)
	}
	if want[p.Asset] == "" {
		return fail(fmt.Errorf("SHA256SUMS doesn't list %s", p.Asset))
	}
	file := filepath.Join(dir, p.Asset)
	if err := download(ctx, a.URL, file); err != nil {
		return fail(err)
	}
	got, err := fileSum(file)
	if err != nil {
		return fail(err)
	}
	if got != want[p.Asset] {
		return fail(fmt.Errorf("%s doesn't match its checksum", p.Asset))
	}
	return &Staged{Plan: p, File: file, Dir: dir}, nil
}

// Apply puts the new copy in place (where that can be done while this one
// runs) and returns the command that finishes the job and starts the new
// copy once process pid has quit. The caller starts it, then quits.
func (s *Staged) Apply(pid int) (*exec.Cmd, error) {
	p := s.Plan
	switch p.Kind {
	case MacApp:
		out := filepath.Join(s.Dir, "new")
		if b, err := exec.Command("ditto", "-x", "-k", s.File, out).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("unzip: %v: %s", err, b)
		}
		app := filepath.Join(out, "code-speed.app")
		if _, err := os.Stat(filepath.Join(app, "Contents", "MacOS", "code-speed")); err != nil {
			return nil, errors.New("the download has no code-speed.app")
		}
		old := filepath.Join(s.Dir, "old.app")
		if err := os.Rename(p.Target, old); err != nil {
			return nil, err
		}
		if err := os.Rename(app, p.Target); err != nil {
			os.Rename(old, p.Target)
			return nil, err
		}
		return detached(exec.Command("/bin/sh", "-c",
			`while kill -0 "$1" 2>/dev/null; do sleep 0.2; done; open "$2"; rm -rf "$3"`,
			"sh", strconv.Itoa(pid), p.Target, s.Dir)), nil
	case LinuxBinary:
		if err := extractTo(s.File, "code-speed", p.Target+".new"); err != nil {
			return nil, err
		}
		if err := os.Rename(p.Target+".new", p.Target); err != nil {
			return nil, err
		}
		os.RemoveAll(s.Dir)
		return detached(exec.Command("/bin/sh", "-c",
			`while kill -0 "$1" 2>/dev/null; do sleep 0.2; done; exec "$2" >/dev/null 2>&1`,
			"sh", strconv.Itoa(pid), p.Target)), nil
	case WinPortable:
		if err := extractTo(s.File, "code-speed.exe", p.Target+".new"); err != nil {
			return nil, err
		}
		// a running .exe can be renamed, not overwritten
		os.Remove(p.Target + ".old")
		if err := os.Rename(p.Target, p.Target+".old"); err != nil {
			return nil, err
		}
		if err := os.Rename(p.Target+".new", p.Target); err != nil {
			os.Rename(p.Target+".old", p.Target)
			return nil, err
		}
		os.RemoveAll(s.Dir)
		return detached(powershell(fmt.Sprintf(`Wait-Process -Id %d -ErrorAction SilentlyContinue; Start-Process -FilePath %s`,
			pid, psQuote(p.Target)))), nil
	case WinSetup:
		// the setup replaces the files once this copy has quit, into the
		// same folder (/D must come last, unquoted)
		return detached(powershell(fmt.Sprintf(
			`Wait-Process -Id %d -ErrorAction SilentlyContinue; Start-Process -FilePath %s -ArgumentList '/S','/D=%s' -Wait; Start-Process -FilePath %s; Remove-Item -Recurse -Force %s`,
			pid, psQuote(s.File), strings.ReplaceAll(filepath.Dir(p.Target), "'", "''"), psQuote(p.Target), psQuote(s.Dir)))), nil
	}
	return nil, ErrUnsupported
}

func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func powershell(script string) *exec.Cmd {
	return exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", script)
}

// extractTo copies the file named name (in any folder) out of a .zip or
// .tar.gz archive to dst, executable.
func extractTo(archive, name, dst string) error {
	write := func(r io.Reader) error {
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, io.LimitReader(r, maxAsset))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return err
	}
	if strings.HasSuffix(archive, ".zip") {
		z, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer z.Close()
		for _, f := range z.File {
			if filepath.Base(f.Name) == name && !f.FileInfo().IsDir() {
				r, err := f.Open()
				if err != nil {
					return err
				}
				defer r.Close()
				return write(r)
			}
		}
		return fmt.Errorf("%s isn't in the download", name)
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
			return fmt.Errorf("%s isn't in the download", name)
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == name {
			return write(tr)
		}
	}
}

// CleanOld removes what a portable Windows update left beside exe.
func CleanOld(exe string) { os.Remove(exe + ".old") }
