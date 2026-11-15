package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// Every change the library makes to an agent's files is backed up first:
// one folder per apply under <appdir>/backups, holding each file and skill
// folder as it was and a manifest of where each goes back to. The library's
// own library.json is kept the same way, so a restore takes the library
// back with the agents.

// Backup is one backup folder's manifest.
type Backup struct {
	ID      string        `json:"id"` // the folder's name, a sortable time
	Time    time.Time     `json:"time"`
	What    string        `json:"what"` // what the apply did, for the list
	Entries []BackupEntry `json:"entries"`
}

// BackupEntry is one path as it was: Saved is its copy in the backup
// folder, "" when it wasn't there (a restore removes it).
type BackupEntry struct {
	Path  string `json:"path"`
	Saved string `json:"saved,omitempty"`
	Dir   bool   `json:"dir,omitempty"`
}

// keepBackups is how many backups are kept, or how many days, whichever
// keeps more.
const (
	keepBackups = 30
	keepDays    = 30
)

type backup struct {
	dir  string
	b    Backup
	seen map[string]bool
}

func newBackup(root, what string, now time.Time) (*backup, error) {
	id := now.UTC().Format("20060102T150405.000Z")
	dir := filepath.Join(root, id)
	for i := 1; exists(dir); i++ {
		dir = filepath.Join(root, id+"-"+strconv.Itoa(i))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &backup{dir: dir, b: Backup{ID: filepath.Base(dir), Time: now, What: what}, seen: map[string]bool{}}, nil
}

// save keeps path as it is now (a file or a folder, or its absence), once.
func (b *backup) save(path string) error {
	if b.seen[path] {
		return nil
	}
	b.seen[path] = true
	e := BackupEntry{Path: path}
	st, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	default:
		e.Saved = strconv.Itoa(len(b.b.Entries))
		to := filepath.Join(b.dir, e.Saved)
		if st.IsDir() || st.Mode()&fs.ModeSymlink != 0 && isDir(path) {
			e.Dir = true
			src := path
			if real, err := filepath.EvalSymlinks(path); err == nil {
				src = real
			}
			err = copyDir(src, to)
		} else {
			err = copyFile(path, to)
		}
		if err != nil {
			return fmt.Errorf("backing up %s: %w", path, err)
		}
	}
	b.b.Entries = append(b.b.Entries, e)
	return nil
}

// done writes the manifest. A backup with nothing in it is removed.
func (b *backup) done() error {
	if len(b.b.Entries) == 0 {
		return os.RemoveAll(b.dir)
	}
	data, err := json.MarshalIndent(b.b, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(b.dir, "manifest.json"), data, 0o600)
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, st.Mode().Perm()|0o600)
}

// listBackups are the backups under root, newest first.
func listBackups(root string) ([]Backup, error) {
	ents, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Backup
	for _, e := range ents {
		data, err := os.ReadFile(filepath.Join(root, e.Name(), "manifest.json"))
		if err != nil {
			continue
		}
		var b Backup
		if json.Unmarshal(data, &b) == nil {
			b.ID = e.Name()
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// prune removes backups beyond keepBackups that are older than keepDays.
func pruneBackups(root string, now time.Time) {
	bs, err := listBackups(root)
	if err != nil {
		return
	}
	for i, b := range bs {
		if i >= keepBackups && now.Sub(b.Time) > keepDays*24*time.Hour {
			os.RemoveAll(filepath.Join(root, b.ID))
		}
	}
}

// restoreBackup puts each path of backup id back as it was. What is there
// now is backed up first (into a new backup), so a restore can be undone.
func restoreBackup(root, id string, now time.Time) error {
	if id == "" || filepath.Base(id) != id {
		return fmt.Errorf("no backup %q", id)
	}
	dir := filepath.Join(root, id)
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return fmt.Errorf("no backup %q", id)
	}
	var b Backup
	if err := json.Unmarshal(data, &b); err != nil {
		return err
	}
	undo, err := newBackup(root, "restore "+id, now)
	if err != nil {
		return err
	}
	for _, e := range b.Entries {
		if err := undo.save(e.Path); err != nil {
			return err
		}
	}
	if err := undo.done(); err != nil {
		return err
	}
	var errs []error
	for _, e := range b.Entries {
		if e.Saved == "" {
			if err := removeDir(e.Path); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		from := filepath.Join(dir, e.Saved)
		if e.Dir {
			err = putDir(from, e.Path)
		} else {
			err = putFile(from, e.Path)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.Path, err))
		}
	}
	return errors.Join(errs...)
}

// putFile replaces dst with a copy of src through a temporary file.
func putFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return writeAtomic(dst, data, 0o600)
}

// writeAtomic writes through a temporary file, keeping an existing file's
// mode.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".code-speed.tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
