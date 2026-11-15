// Ported from magpie (internal/library/mcp.go: entries, parseEntries,
// openCodeLayout, putEntry, del, putCodex, delCodex), MIT License,
// (c) 2026 yetone.

package mcpfmt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/tidwall/jsonc"
	"gopkg.in/yaml.v3"

	"github.com/daemonx666/code-speed/internal/edit"
)

// File is the file an agent keeps its MCP servers in, and the format of it.
type File struct {
	Path   string
	Format Format
}

// parsed is what a file holds under its servers' key.
type parsed struct {
	entries map[string]map[string]any
	// OpenCode only: where the entries are. v1 are mcp.<name>, v2 are
	// OpenCode 2's mcp.servers.<name>, native whether mcp.servers exists.
	v1, v2 map[string]bool
	native bool
}

// parse reads the file and finds every entry under the servers' key. A
// missing or empty file holds none; a file that doesn't parse, or whose
// servers' key is not an object, is an error, so nothing is ever written
// over what can't be read.
func (f File) parse() (*parsed, error) {
	if !f.Format.known() {
		return nil, fmt.Errorf("unknown format %q", string(f.Format))
	}
	p := &parsed{entries: map[string]map[string]any{}}
	raw, err := edit.Read(f.Path)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return p, nil
	}
	var doc map[string]any
	switch {
	case f.Format.toml():
		// only the servers' tables are read: not Codex's thousands of projects
		err = toml.Unmarshal(edit.TOMLWithout(raw, "projects"), &doc)
	case f.Format.yaml():
		err = yaml.Unmarshal(raw, &doc)
	default:
		dec := json.NewDecoder(bytes.NewReader(jsonc.ToJSON(raw)))
		dec.UseNumber() // a number the user wrote comes back as written
		if err = dec.Decode(&doc); err == nil {
			if _, e := dec.Token(); !errors.Is(e, io.EOF) {
				err = errors.New("unexpected data after the top-level value")
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.Path, err)
	}
	if doc == nil {
		return nil, fmt.Errorf("%s: the top level is not an object", f.Path)
	}
	// the key can be a path: ZCode keeps them in mcp.servers
	var all map[string]any = doc
	for _, k := range strings.Split(f.Format.key(), ".") {
		v, ok := all[k]
		if !ok || v == nil {
			return p, nil
		}
		if all, ok = v.(map[string]any); !ok {
			return nil, fmt.Errorf("%s: %s is not an object, so its servers can't be read or written", f.Path, f.Format.key())
		}
	}
	servers := openCodeServers(f.Format, all)
	p.native = servers != nil
	p.v1, p.v2 = map[string]bool{}, map[string]bool{}
	for name, v := range all {
		if m, ok := v.(map[string]any); ok && (servers == nil || name != "servers") {
			p.entries[name] = m
			p.v1[name] = true
		}
	}
	// OpenCode 2's own entries win a name over a V1 one, as OpenCode reads
	// them
	for name, v := range servers {
		if m, ok := v.(map[string]any); ok {
			p.entries[name] = m
			p.v2[name] = true
		}
	}
	return p, nil
}

// openCodeServers is OpenCode 2's mcp.servers, where its native config and
// its own `opencode mcp add` keep the servers, or nil. OpenCode 2 reads the
// V1 shape, mcp.<name>, beside it, so one file can hold both, and "servers"
// there is that object, not a server called servers: one with a type,
// command or url of its own is a V1 server by that name.
func openCodeServers(format Format, mcp map[string]any) map[string]any {
	if format != OpenCode {
		return nil
	}
	m, ok := mcp["servers"].(map[string]any)
	if !ok {
		return nil
	}
	for _, k := range []string{"type", "command", "url"} {
		if _, own := m[k]; own {
			return nil
		}
	}
	return m
}

// path is the key path of the server's entry for a write: OpenCode 2's
// mcp.servers.<name> once the file keeps its servers there (the entry there
// already, or a new one), else <key>.<name>.
func (f File) path(p *parsed, name string) (string, bool) {
	if f.Format == OpenCode && p.native && (p.v2[name] || !p.v1[name]) {
		return "mcp.servers." + name, true
	}
	return f.Format.key() + "." + name, false
}

// Read returns every server the file holds, decoded, by name: those of the
// library and the user's own alike. Entries that can't be read as a server (a
// Goose builtin, Zed-style extension entries) are left out. A missing file
// holds none; a file that doesn't parse is an error.
func (f File) Read() (map[string]*Server, error) {
	p, err := f.parse()
	if err != nil {
		return nil, err
	}
	out := map[string]*Server{}
	for name, m := range p.entries {
		if s, ok := f.Format.decode(name, m); ok {
			out[name] = s
		}
	}
	return out, nil
}

// Same reports whether a and b are the same server as this file can tell:
// their Hash is equal, but for the formats that give no way to say SSE
// (OpenCode's remote, Cursor's url: the agent tries streamable HTTP and falls
// back to SSE itself), where an SSE server reads back as http and so does
// not count as different from one.
func (f File) Same(a, b *Server) bool {
	return f.Format.flat(a).Hash() == f.Format.flat(b).Hash()
}

// flat is s as the format reads it back: an SSE server as an http one where
// the format can't name SSE.
func (f Format) flat(s *Server) *Server {
	if s.Transport != "sse" || f != OpenCode && f != Cursor {
		return s
	}
	c := *s
	c.Transport = "http"
	return &c
}

// Supports says why the format can't hold the server, or nil.
func (f File) Supports(s *Server) error { return f.Format.supports(s) }

// Put adds the servers to the file, or replaces those it has by their names.
// Everything else stays: other servers, the keys of a replaced entry that
// aren't what makes it that server (timeouts, disabled, tools), comments,
// order and indentation. A server the file already holds as it is is left
// untouched. The file and its folders are made when missing. The file is
// written atomically, read again to check it parses and holds each server as
// given, and put back as it was, with an error, when it doesn't.
func (f File) Put(servers ...*Server) error {
	var todo []*Server
	for _, s := range servers {
		if s == nil {
			return errors.New("nil server")
		}
		if err := s.Validate(); err != nil {
			return err
		}
		if err := f.Format.supports(s); err != nil {
			return fmt.Errorf("%s: %w", s.Name, err)
		}
		// the last of a name given twice is the one written
		todo = slices.DeleteFunc(todo, func(o *Server) bool { return o.Name == s.Name })
		todo = append(todo, s)
	}
	if len(todo) == 0 {
		return nil
	}
	return edit.Atomically(func() error {
		p, err := f.parse()
		if err != nil {
			return err
		}
		var write []*Server
		for _, s := range todo {
			if old := p.entries[s.Name]; old != nil {
				if cur, ok := f.Format.current(s.Name, old, s); ok && f.Same(cur, s) {
					continue
				}
			}
			write = append(write, s)
		}
		if len(write) == 0 {
			return nil
		}
		if err := f.write(p, write); err != nil {
			return err
		}
		// read back: what was written parses, and says what was meant
		back, err := f.parse()
		if err != nil {
			return fmt.Errorf("%s: written file does not parse: %w", f.Path, err)
		}
		for _, s := range write {
			cur, ok := f.Format.current(s.Name, back.entries[s.Name], s)
			if back.entries[s.Name] == nil || !ok || !f.Same(cur, s) {
				return fmt.Errorf("%s: %s did not read back as written", f.Path, s.Name)
			}
		}
		return nil
	}, f.Path)
}

func (f File) write(p *parsed, servers []*Server) error {
	switch {
	case f.Format.toml():
		for _, s := range servers {
			if err := putTOML(f.Path, s.Name, f.Format.merged(s, p.entries[s.Name])); err != nil {
				return err
			}
		}
		return nil
	case f.Format.yaml():
		kvs := make([]edit.KV, len(servers))
		for i, s := range servers {
			path, _ := f.path(p, s.Name)
			kvs[i] = edit.KV{Path: path, Value: f.Format.merged(s, p.entries[s.Name])}
		}
		return edit.SetYAML(f.Path, kvs...)
	}
	kvs := make([]edit.KV, len(servers))
	for i, s := range servers {
		path, native := f.path(p, s.Name)
		o := f.Format.merged(s, p.entries[s.Name])
		// OpenCode 2's servers have no enabled: on unless disabled, which is
		// the user's to set
		if native {
			o = slices.DeleteFunc(o, func(e kv) bool { return e.k == "enabled" })
		}
		kvs[i] = edit.KV{Path: path, Value: o}
	}
	return edit.SetJSON(f.Path, kvs...)
}

// putTOML replaces the server's table and takes out the tables under it
// ([mcp_servers.x.env]), which the new table writes inline instead.
func putTOML(path, name string, o ordered) error {
	table := "mcp_servers." + name
	if err := edit.DelTOMLTablesUnder(path, table+"."); err != nil {
		return err
	}
	kvs := make([]edit.KV, len(o))
	for i, e := range o {
		kvs[i] = edit.KV{Path: tomlKey(e.k), Value: edit.Raw(tomlValue(e.v))}
	}
	return edit.SetTOMLTable(path, table, kvs...)
}

// Remove takes the servers by those names out of the file and leaves
// everything else as it is. A name the file doesn't have is skipped. Like
// Put, it checks the result and puts the file back when that fails.
func (f File) Remove(names ...string) error {
	for _, n := range names {
		if err := checkName(n); err != nil {
			return err
		}
	}
	return edit.Atomically(func() error {
		p, err := f.parse()
		if err != nil {
			return err
		}
		var present []string
		for _, n := range names {
			if p.entries[n] != nil && !slices.Contains(present, n) {
				present = append(present, n)
			}
		}
		if len(present) == 0 {
			return nil
		}
		if err := f.del(p, present); err != nil {
			return err
		}
		back, err := f.parse()
		if err != nil {
			return fmt.Errorf("%s: written file does not parse: %w", f.Path, err)
		}
		for _, n := range present {
			if back.entries[n] != nil {
				return fmt.Errorf("%s: %s is still there after removing it (it is not written as a table of its own)", f.Path, n)
			}
		}
		return nil
	}, f.Path)
}

func (f File) del(p *parsed, names []string) error {
	var paths []string
	for _, n := range names {
		if f.Format == OpenCode {
			// from wherever OpenCode reads it: mcp.<name>, OpenCode 2's
			// mcp.servers.<name>, or both, and never mcp.servers itself
			if p.v1[n] {
				paths = append(paths, "mcp."+n)
			}
			if p.v2[n] {
				paths = append(paths, "mcp.servers."+n)
			}
			continue
		}
		paths = append(paths, f.Format.key()+"."+n)
	}
	switch {
	case f.Format.toml():
		for _, n := range names {
			// the server's table and the tables under it ([…env]); child
			// array tables would otherwise recreate the removed server
			table := "mcp_servers." + n
			if err := edit.DelTOMLTablesUnder(f.Path, table+"."); err != nil {
				return err
			}
			if err := edit.DelTOMLTable(f.Path, table); err != nil {
				return err
			}
		}
		return nil
	case f.Format.yaml():
		return edit.DelYAML(f.Path, paths...)
	}
	return edit.DelJSON(f.Path, paths...)
}
