// Ported from magpie (internal/edit/file.go), MIT License, (c) 2026 yetone.
// Trimmed to what the MCP library needs; see the package comment in file.go.

// Package edit provides format-preserving editors for the config files that
// coding agents keep: JSON/JSONC, TOML and YAML. Only the requested key
// changes; comments, ordering and indentation are left as they are (YAML is
// re-rendered from its node tree, so it keeps comments and order but gets
// two-space indentation). Every write is atomic and checked before it lands.
package edit

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Read returns the file contents, or (nil, nil) when the file does not exist.
//
// A UTF-8 byte order mark at the start is not part of what is returned:
// Windows tools (PowerShell's UTF8 encoding, Notepad) put one in front of an
// agent's settings.json or config.toml, which the agent reads through while a
// JSON or TOML parser refuses it. WriteAtomic puts it back, see keepBOM.
func Read(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return TrimBOM(b), nil
}

// bom is the UTF-8 byte order mark.
var bom = []byte("\xef\xbb\xbf")

// TrimBOM is b without a UTF-8 byte order mark at its start, for a config
// read some other way than Read.
func TrimBOM(b []byte) []byte { return bytes.TrimPrefix(b, bom) }

// keepBOM is data with the byte order mark the file at path starts with,
// when it starts with one and data doesn't: the user's file keeps the
// encoding it was saved in, for the tools that wrote it that way.
func keepBOM(path string, data []byte) []byte {
	if len(data) == 0 || bytes.HasPrefix(data, bom) {
		return data
	}
	f, err := os.Open(path)
	if err != nil {
		return data
	}
	defer f.Close()
	head := make([]byte, len(bom))
	if n, _ := io.ReadFull(f, head); n < len(bom) || !bytes.Equal(head, bom) {
		return data
	}
	return append(append(make([]byte, 0, len(bom)+len(data)), bom...), data...)
}

// WriteAtomic writes data to path via a temp file + rename so a crash can
// never leave a half-written config behind. File mode and group are
// preserved. When path is a symlink (a config kept in a dotfiles repo) the
// file it points at is written and the link stays; a file with other hard
// links is written in place, see writeInPlace. Missing folders are made.
func WriteAtomic(path string, data []byte) error {
	path, err := Target(path)
	if err != nil {
		return err
	}
	data = keepBOM(path, data)
	// A file that already holds these bytes is left alone: a new mtime
	// wakes every watcher of it, and a rename swaps the file under an agent
	// reading it, for nothing.
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, data) {
		return nil
	}
	if hardLinked(path) {
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err == nil {
			return writeInPlace(f, data)
		}
		// A file that can't be opened for writing (mode 0444) is renamed
		// over instead, which splits this name from the others; opened
		// first, so nothing is made beside it on the way.
		if !errors.Is(err, fs.ErrPermission) {
			return err
		}
	}
	mode := fs.FileMode(0o644)
	st, statErr := os.Stat(path)
	if statErr == nil {
		mode = st.Mode().Perm()
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if statErr == nil {
		keepGroup(tmp, st)
	}
	// on the disk before it is renamed in: a machine that goes down between
	// the two can leave the file at its length, all zero
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// rename moves tmp over path. On Windows a rename onto a file an editor, an
// antivirus scanner or the agent itself has open fails for a moment, so it
// is tried again a few times before giving up.
func rename(tmp, path string) error {
	err := os.Rename(tmp, path)
	for i := 0; err != nil && runtime.GOOS == "windows" && i < 10; i++ {
		time.Sleep(time.Duration(i+1) * 20 * time.Millisecond)
		err = os.Rename(tmp, path)
	}
	return err
}

// writeInPlace writes data into f, opened for writing, and closes it: for
// a file that has other names too (a hard link), where a new file renamed
// over one name would split it from the others, each left with its own text.
//
// Writing in place is not atomic, so the text first goes to a temp file
// beside it, synced to disk, and only then into the file: a crash part way
// leaves the whole new text in that temp file, and a write that fails part
// way keeps it there and names it in the error.
func writeInPlace(f *os.File, data []byte) error {
	path := f.Name()
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		f.Close()
		return err
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if e := tmp.Close(); err == nil {
		err = e
	}
	if err != nil {
		f.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	// Written over and then cut to length, so a reader sees the new head over
	// the old text rather than an empty file, which a tool may load as a
	// config with nothing set and act on, where a mangled one fails to parse.
	_, err = f.Write(data)
	if err == nil {
		err = f.Truncate(int64(len(data)))
	}
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return fmt.Errorf("%w; the new text is kept in %s", err, tmp.Name())
	}
	_ = os.Remove(tmp.Name())
	return nil
}

// Target is the file a write to path should replace: path itself, or,
// when path is a symlink, the file at the end of its links — the path it
// names even when nothing is there yet. Renaming over the link instead
// would turn it into a file of its own and leave its target as it was.
func Target(path string) (string, error) {
	if p, err := filepath.EvalSymlinks(path); err == nil {
		return p, nil
	}
	p := path
	for range 255 {
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			return p, nil
		}
		dest, err := os.Readlink(p)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(dest) {
			// not Join: its cleaning would take a ".." past a linked folder
			dest = filepath.Dir(p) + string(filepath.Separator) + dest
		}
		p = dest
	}
	return "", fmt.Errorf("%s: too many links", path)
}

// Atomically runs fn, which may write the files at paths in several steps,
// and puts each of them back as it was — its bytes and mode, or its absence
// — when fn fails, so an edit that fails part way leaves no file half made.
// A file that can't be read beforehand is left to fn as it is.
func Atomically(fn func() error, paths ...string) error {
	type saved struct {
		path   string
		data   []byte
		mode   fs.FileMode
		exists bool
	}
	var before []saved
	for _, p := range paths {
		st, err := os.Stat(p)
		if errors.Is(err, fs.ErrNotExist) {
			if t, err := Target(p); err == nil {
				p = t // a link to nothing yet stays, only what fn made goes
			}
			before = append(before, saved{path: p})
			continue
		}
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		before = append(before, saved{p, b, st.Mode().Perm(), true})
	}
	err := fn()
	if err == nil {
		return nil
	}
	for _, s := range before {
		now, rerr := os.ReadFile(s.path)
		switch {
		case !s.exists:
			if !errors.Is(rerr, fs.ErrNotExist) {
				if e := os.Remove(s.path); e != nil && !errors.Is(e, fs.ErrNotExist) {
					err = errors.Join(err, fmt.Errorf("put %s back: %w", s.path, e))
				}
			}
		case rerr != nil || !bytes.Equal(now, s.data):
			if e := WriteAtomic(s.path, s.data); e != nil {
				err = errors.Join(err, fmt.Errorf("put %s back: %w", s.path, e))
			} else if e := os.Chmod(s.path, s.mode); e != nil {
				err = errors.Join(err, fmt.Errorf("put %s back: %w", s.path, e))
			}
		}
	}
	return err
}
