package source

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/daemonx666/code-speed/internal/source/zstdread"
	"github.com/daemonx666/code-speed/internal/usage"
)

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

// Found is a log file and when it last changed.
type Found struct {
	Source  usage.Source
	Path    string
	ModTime time.Time
	Size    int64
}

var dayDir = regexp.MustCompile(`(?:^|/)(\d{4})/(\d{2})/(\d{2})$`)

// Discover lists every .jsonl log under roots changed at or after since.
// Codex's year/month/day folders older than since are skipped whole, so a
// long history costs nothing to walk.
func Discover(roots []Root, since time.Time) []Found {
	var out []Found
	cutoff := since.AddDate(0, 0, -1).Format("2006/01/02")
	for _, r := range roots {
		_ = filepath.WalkDir(r.Dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // a folder that can't be read is skipped, not fatal
			}
			if d.IsDir() {
				rel, _ := filepath.Rel(r.Dir, path)
				if m := dayDir.FindStringSubmatch(filepath.ToSlash(rel)); m != nil && m[1]+"/"+m[2]+"/"+m[3] < cutoff {
					return filepath.SkipDir
				}
				return nil
			}
			if spec, ok := Lookup(r.Source); !ok || spec.Match == nil || !spec.Match(path) {
				return nil
			}
			info, err := d.Info()
			if err != nil || info.ModTime().Before(since) {
				return nil
			}
			out = append(out, Found{Source: r.Source, Path: path, ModTime: info.ModTime(), Size: info.Size()})
			return nil
		})
	}
	return out
}

// Tail reads a log file incrementally: each Read parses only the bytes
// appended since the last one. A partial last line waits for its end.
type Tail struct {
	Path    string
	Source  usage.Source
	Parser  Parser
	offset  int64
	partial []byte
	skip    bool // inside a line longer than MaxLine
	ModTime time.Time
	Size    int64
	zstRead bool // a .zst file was read once, at ModTime and Size
}

func NewTail(f Found, d *Dedup, sink Sink) *Tail {
	return &Tail{Path: f.Path, Source: f.Source, Parser: New(f.Source, f.Path, d, sink)}
}

// Read parses what was appended. A file that shrank was rewritten: it is
// read again from the start with a fresh parser (the shared dedup set
// keeps its responses from counting twice).
func (t *Tail) Read(d *Dedup, sink Sink) error {
	fi, err := os.Stat(t.Path)
	if err != nil {
		return err
	}
	if strings.HasSuffix(t.Path, ".zst") {
		return t.readZst(fi, d, sink)
	}
	t.ModTime = fi.ModTime()
	if fi.Size() < t.offset {
		t.offset, t.partial, t.skip = 0, nil, false
		t.Parser = New(t.Source, t.Path, d, sink)
	}
	t.Size = fi.Size()
	if fi.Size() == t.offset {
		return nil
	}
	f, err := os.Open(t.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return err
	}
	buf := make([]byte, 256<<10)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			t.offset += int64(n)
			t.feed(buf[:n])
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (t *Tail) feed(b []byte) {
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			if !t.skip {
				t.partial = append(t.partial, b...)
				if len(t.partial) > MaxLine {
					t.partial, t.skip = nil, true
				}
			}
			return
		}
		if t.skip {
			t.skip = false
		} else if len(t.partial) > 0 {
			line := append(t.partial, b[:i]...)
			if len(line) <= MaxLine {
				t.Parser.Line(line)
			}
			t.partial = nil
		} else {
			t.Parser.Line(b[:i])
		}
		b = b[i+1:]
	}
}

// readZst reads an archived rollout (.jsonl.zst): static, so it is
// decompressed whole, and again, with a fresh parser, only when its size
// or modification time changes.
func (t *Tail) readZst(fi os.FileInfo, d *Dedup, sink Sink) error {
	if t.zstRead && fi.Size() == t.Size && fi.ModTime().Equal(t.ModTime) {
		return nil
	}
	if t.zstRead {
		t.Parser = New(t.Source, t.Path, d, sink)
	}
	t.zstRead, t.ModTime, t.Size = true, fi.ModTime(), fi.Size()
	data, err := zstdread.File(t.Path)
	if err != nil && len(data) == 0 {
		return err
	}
	// a damaged tail still yields the lines before it
	t.partial, t.skip = nil, false
	t.feed(data)
	if len(t.partial) > 0 {
		t.Parser.Line(t.partial)
		t.partial = nil
	}
	return err
}
