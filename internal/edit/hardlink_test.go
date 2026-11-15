// Ported from magpie (internal/edit/hardlink_test.go), MIT License, (c) 2026 yetone.

package edit

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// hardLink makes link another name of target, skipping where the file
// system has no hard links.
func hardLink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Link(target, link); err != nil {
		t.Skip("no hard links here:", err)
	}
}

func sameFile(t *testing.T, a, b string) {
	t.Helper()
	fa, err := os.Stat(a)
	if err != nil {
		t.Fatal(err)
	}
	fb, err := os.Stat(b)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(fa, fb) {
		t.Fatalf("%s and %s are no longer one file", a, b)
	}
}

// A config another tool keeps as a hard link (Orca links omp's models.yml
// into its overlay): a write goes into the file itself, so every name has
// the new text, the mode stays, and nothing is left beside it.
func TestWriteAtomicKeepsHardLink(t *testing.T) {
	d := t.TempDir()
	overlay := filepath.Join(d, "overlay.yml")
	if err := os.WriteFile(overlay, []byte("providers: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(overlay)
	p := filepath.Join(d, "models.yml")
	hardLink(t, overlay, p)
	for _, text := range []string{"providers:\n  magpie:\n    api: openai-completions\n", "x: 1\n"} {
		if err := WriteAtomic(p, []byte(text)); err != nil {
			t.Fatal(err)
		}
		sameFile(t, p, overlay)
		if got := read(t, overlay); got != text {
			t.Fatalf("other name: %q, want %q", got, text)
		}
	}
	if now, _ := os.Stat(p); now.Mode().Perm() != st.Mode().Perm() {
		t.Errorf("mode %v, was %v", now.Mode().Perm(), st.Mode().Perm())
	}
	if es, _ := os.ReadDir(d); len(es) != 2 {
		t.Errorf("left beside it: %v", es)
	}
}

// A failed edit puts a hard-linked file back in place, still one file.
func TestAtomicallyKeepsHardLink(t *testing.T) {
	d := t.TempDir()
	other := filepath.Join(d, "other.json")
	if err := os.WriteFile(other, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "settings.json")
	hardLink(t, other, p)
	err := Atomically(func() error {
		if err := WriteAtomic(p, []byte("half")); err != nil {
			return err
		}
		return errors.New("fail")
	}, p)
	if err == nil || err.Error() != "fail" {
		t.Fatalf("err %v", err)
	}
	sameFile(t, p, other)
	if got := read(t, other); got != "{}" {
		t.Errorf("other name %q", got)
	}
}

// A hard-linked file that can't be opened for writing (mode 0444) is
// replaced by rename, as before in-place writes: the write goes through,
// and that name stops being a link of the other.
func TestWriteAtomicReadOnlyHardLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows renames over no read-only file, linked or not")
	}
	if os.Geteuid() == 0 {
		t.Skip("root writes a 0444 file")
	}
	d := t.TempDir()
	other := filepath.Join(d, "other.yml")
	if err := os.WriteFile(other, []byte("old\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	// the umask cuts WriteFile's mode: 0400 under 077 (#3640)
	if err := os.Chmod(other, 0o444); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "models.yml")
	hardLink(t, other, p)
	if err := WriteAtomic(p, []byte("new\n")); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != "new\n" {
		t.Fatalf("got %q", got)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o444 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
}
