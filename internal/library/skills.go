package library

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// A skill is a folder holding a SKILL.md, named for the folder. Agents
// find one by its folder in their skills folder; the library keeps its own
// copy (store) and gives each agent a copy of that.

// FoundSkill is a skill seen in a skills folder.
type FoundSkill struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Hash        string `json:"hash"`
	Path        string `json:"path"`
	// Link says the folder entry is a symbolic link (another tool's, such
	// as magpie's): read through, and never replaced or removed by a copy
	Link bool `json:"link,omitempty"`
}

// maxSkillBytes is how much one skill may hold for the library to copy it.
const maxSkillBytes = 50 << 20

// scanSkills lists the skills in dir: its folders with a SKILL.md (any
// case). A folder that isn't there is no skills.
func scanSkills(dir string) ([]FoundSkill, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []FoundSkill
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		st, err := os.Stat(p) // through a link
		if err != nil || !st.IsDir() {
			continue
		}
		md := skillFile(p)
		if md == "" {
			continue
		}
		h, err := hashDir(p)
		if err != nil {
			continue
		}
		name, desc := frontmatter(md)
		_ = name // the folder's name is the one agents go by
		out = append(out, FoundSkill{Name: e.Name(), Description: desc, Hash: h, Path: p, Link: e.Type()&fs.ModeSymlink != 0})
	}
	return out, nil
}

// skillFile is dir's SKILL.md, "" when it has none.
func skillFile(dir string) string {
	for _, n := range []string{"SKILL.md", "skill.md", "Skill.md"} {
		if p := filepath.Join(dir, n); exists(p) {
			return p
		}
	}
	return ""
}

// frontmatter reads name and description from SKILL.md's YAML front
// matter (--- … ---), "" for what isn't there or can't be read.
func frontmatter(path string) (name, desc string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, 64<<10))
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return "", ""
	}
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "---" {
			break
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(line, " ") {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch strings.TrimSpace(k) {
		case "name":
			name = v
		case "description":
			desc = v
		}
	}
	return name, desc
}

// hashDir is a hash of what dir holds: each file's path below it and its
// bytes, in path order, so the same skill hashes the same wherever it is.
// Links inside are hashed by where they point, not followed.
func hashDir(dir string) (string, error) {
	var files []string
	var total int64
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != dir && strings.HasPrefix(d.Name(), ".git") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == ".DS_Store" {
			return nil
		}
		files = append(files, p)
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if total > maxSkillBytes {
		return "", fmt.Errorf("%s holds %d MB, more than the library copies", dir, total>>20)
	}
	sort.Strings(files)
	h := sha256.New()
	for _, p := range files {
		rel, _ := filepath.Rel(dir, p)
		fmt.Fprintf(h, "%s\x00", filepath.ToSlash(rel))
		if t, err := os.Readlink(p); err == nil {
			fmt.Fprintf(h, "link:%s\x00", t)
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			return "", err
		}
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return "", err
		}
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// copyDir copies src's tree to dst, which must not exist. Links are
// copied as links, never followed.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		to := filepath.Join(dst, rel)
		if d.IsDir() {
			if p != src && strings.HasPrefix(d.Name(), ".git") {
				return filepath.SkipDir
			}
			return os.MkdirAll(to, 0o755)
		}
		if d.Type()&fs.ModeSymlink != 0 {
			t, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(t, to)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

// putDir makes dst a copy of src: copied beside it first, then swapped in,
// so an agent never reads half a skill. src may be a link (read through).
func putDir(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(src); err == nil {
		src = real
	}
	tmp := dst + ".code-speed-new"
	os.RemoveAll(tmp)
	if err := copyDir(src, tmp); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	old := dst + ".code-speed-old"
	os.RemoveAll(old)
	if exists(dst) || isLink(dst) {
		if err := os.Rename(dst, old); err != nil {
			os.RemoveAll(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Rename(old, dst)
		os.RemoveAll(tmp)
		return err
	}
	return os.RemoveAll(old)
}

func isLink(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Mode()&fs.ModeSymlink != 0
}

// removeDir removes a skill's folder (a link itself, not where it points).
func removeDir(p string) error {
	if isLink(p) {
		return os.Remove(p)
	}
	err := os.RemoveAll(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
