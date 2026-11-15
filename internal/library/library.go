// Package library keeps the skills and MCP servers the user's agents
// should have, once, and gives each to the agents ticked for it, in each
// agent's own place and format; unticking one takes it out again. What
// the agents already had is listed until imported, after which it is the
// library's like the rest. Every change to an agent's files is backed up
// first (backup.go), and an entry the user changed in an agent since the
// library wrote it is left alone until they choose (drift).
//
// The model follows magpie's library (github.com/yetone/magpie, MIT,
// internal/library), trimmed to skills and MCP servers.
package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/daemonx666/code-speed/internal/library/mcpfmt"
)

// Kinds of item.
const (
	KindMCP   = "mcp"
	KindSkill = "skill"
)

// Server is a library MCP server and the targets it is given to.
type Server struct {
	mcpfmt.Server
	Agents []string `json:"agents,omitempty"`
}

// Skill is a library skill (its files are in the store) and the targets it
// is given to.
type Skill struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Hash        string   `json:"hash"`
	Agents      []string `json:"agents,omitempty"`
}

// Applied is what the library wrote to (or found and took over in) one
// target: each item's name and the hash of what is there as it wrote it.
// Only these are ever changed or taken out of the target.
type Applied struct {
	MCP    map[string]string `json:"mcp,omitempty"`
	Skills map[string]string `json:"skills,omitempty"`
}

// Library is library.json.
type Library struct {
	Version  int                 `json:"version"`
	MCP      []*Server           `json:"mcp"`
	Skills   []*Skill            `json:"skills"`
	Projects []string            `json:"projects,omitempty"`
	Applied  map[string]*Applied `json:"applied,omitempty"`
}

// Manager reads and changes the library and the agents' files. Its
// methods may be called from any goroutine.
type Manager struct {
	mu    sync.Mutex
	dir   string // code-speed's folder
	now   func() time.Time
	users func() []*Target
}

// New is the manager of the library kept in dir (appdir.Dir()).
func New(dir string) *Manager {
	return &Manager{dir: dir, now: time.Now, users: UserTargets}
}

func (m *Manager) file() string    { return filepath.Join(m.dir, "library.json") }
func (m *Manager) store() string   { return filepath.Join(m.dir, "library", "skills") }
func (m *Manager) backups() string { return filepath.Join(m.dir, "backups") }

func (m *Manager) load() (*Library, error) {
	l := &Library{Version: 1, Applied: map[string]*Applied{}}
	data, err := os.ReadFile(m.file())
	if errors.Is(err, fs.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, l); err != nil {
		return nil, fmt.Errorf("library.json: %w", err)
	}
	if l.Applied == nil {
		l.Applied = map[string]*Applied{}
	}
	// a skill's files may have been changed in the store (its folder is
	// opened from the page): its hash is what is there
	for _, s := range l.Skills {
		if h, err := hashDir(filepath.Join(m.store(), s.Name)); err == nil {
			s.Hash = h
		}
	}
	return l, nil
}

func (m *Manager) save(l *Library) error {
	sort.Slice(l.MCP, func(i, j int) bool { return l.MCP[i].Name < l.MCP[j].Name })
	sort.Slice(l.Skills, func(i, j int) bool { return l.Skills[i].Name < l.Skills[j].Name })
	for id, a := range l.Applied {
		if len(a.MCP) == 0 && len(a.Skills) == 0 {
			delete(l.Applied, id)
		}
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	// it holds the servers' env and headers, keys among them
	return writeAtomic(m.file(), append(data, '\n'), 0o600)
}

func (l *Library) applied(target string) *Applied {
	a := l.Applied[target]
	if a == nil {
		a = &Applied{}
		l.Applied[target] = a
	}
	if a.MCP == nil {
		a.MCP = map[string]string{}
	}
	if a.Skills == nil {
		a.Skills = map[string]string{}
	}
	return a
}

func (l *Library) server(name string) *Server {
	for _, s := range l.MCP {
		if s.Name == name {
			return s
		}
	}
	return nil
}

func (l *Library) skill(name string) *Skill {
	for _, s := range l.Skills {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// targets are the user's targets and each project's.
func (m *Manager) targets(l *Library) []*Target {
	users := m.users()
	out := slices.Clone(users)
	for _, p := range l.Projects {
		out = append(out, ProjectTargets(p, users)...)
	}
	return out
}

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// CheckName says whether name can be an item's name in every agent.
func CheckName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("名称只能用字母、数字、- 和 _，不超过 64 个字符：%q", name)
	}
	return nil
}

// ---- reading the targets ----------------------------------------------

// held is what one target holds now.
type held struct {
	mcp    map[string]*mcpfmt.Server
	mcpErr error
	skills map[string]FoundSkill
	sklErr error
}

func readTarget(t *Target) held {
	var h held
	if t.MCP != nil {
		h.mcp, h.mcpErr = t.MCP.Read()
	}
	if t.Skills != "" {
		var list []FoundSkill
		list, h.sklErr = scanSkills(t.Skills)
		h.skills = map[string]FoundSkill{}
		for _, s := range list {
			h.skills[s.Name] = s
		}
	}
	return h
}

// ---- state ---------------------------------------------------------------

// Cell states: what an item is in one target.
const (
	CellOn          = "on"          // there, as the library has it
	CellOff         = "off"         // not there, not ticked
	CellAdd         = "add"         // ticked, not there yet: the next apply writes it
	CellUpdate      = "update"      // ticked, there in an older version the library wrote: rewritten
	CellRemove      = "remove"      // unticked, there as the library wrote it: taken out
	CellDrift       = "drift"       // changed in the agent since the library wrote it: left alone
	CellForeign     = "foreign"     // the agent has its own by that name: left alone
	CellLink        = "link"        // a link another tool made: left alone
	CellUnsupported = "unsupported" // the target can't take it (Reason)
	CellError       = "error"       // the target's file can't be read (Reason)
)

// Cell is one item in one target.
type Cell struct {
	State  string `json:"state"`
	Want   bool   `json:"want"`
	Reason string `json:"reason,omitempty"`
}

// Item is a library item as the page shows it.
type Item struct {
	Kind        string           `json:"kind"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Hash        string           `json:"hash"`
	Server      *mcpfmt.Server   `json:"server,omitempty"`
	Path        string           `json:"path,omitempty"` // a skill's folder in the store
	Cells       map[string]*Cell `json:"cells"`
}

// Found is something the agents hold that the library doesn't: one
// version of it (by hash), and where it is.
type Found struct {
	Kind        string         `json:"kind"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Hash        string         `json:"hash"`
	In          []string       `json:"in"`
	Server      *mcpfmt.Server `json:"server,omitempty"`
	Path        string         `json:"path,omitempty"`
	// Conflict: another version by the same name is in an agent or in the
	// library, so importing this one needs a name of its own (As)
	Conflict bool `json:"conflict,omitempty"`
}

// TargetState is a target and whether its places can be read.
type TargetState struct {
	*Target
	Error string `json:"error,omitempty"`
}

// State is everything the page shows.
type State struct {
	Targets  []TargetState `json:"targets"`
	MCP      []*Item       `json:"mcp"`
	Skills   []*Item       `json:"skills"`
	Found    []*Found      `json:"found"`
	Projects []string      `json:"projects"`
	Pending  int           `json:"pending"` // cells the next apply changes
}

// State reads every target and says what each item is in each.
func (m *Manager) State() (*State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, err := m.load()
	if err != nil {
		return nil, err
	}
	return m.state(l), nil
}

func (m *Manager) state(l *Library) *State {
	ts := m.targets(l)
	st := &State{Projects: slices.Clone(l.Projects), MCP: []*Item{}, Skills: []*Item{}, Found: []*Found{}}
	holds := map[string]held{}
	for _, t := range ts {
		h := readTarget(t)
		holds[t.ID] = h
		ts := TargetState{Target: t}
		if errs := errors.Join(h.mcpErr, h.sklErr); errs != nil {
			ts.Error = errs.Error()
		}
		st.Targets = append(st.Targets, ts)
	}

	for _, s := range l.MCP {
		srv := s.Server
		it := &Item{Kind: KindMCP, Name: s.Name, Hash: s.Hash(), Server: &srv, Cells: map[string]*Cell{}}
		for _, t := range ts {
			it.Cells[t.ID] = mcpCell(l, t, holds[t.ID], s)
		}
		st.MCP = append(st.MCP, it)
	}
	for _, s := range l.Skills {
		it := &Item{Kind: KindSkill, Name: s.Name, Description: s.Description, Hash: s.Hash, Path: filepath.Join(m.store(), s.Name), Cells: map[string]*Cell{}}
		for _, t := range ts {
			it.Cells[t.ID] = skillCell(l, t, holds[t.ID], s)
		}
		st.Skills = append(st.Skills, it)
	}
	for _, it := range append(slices.Clone(st.MCP), st.Skills...) {
		for _, c := range it.Cells {
			switch c.State {
			case CellAdd, CellUpdate, CellRemove:
				st.Pending++
			}
		}
	}
	// what was taken out of the library but is still in a target it wrote
	// to is taken out at the next apply
	for id, a := range l.Applied {
		for n := range a.MCP {
			if l.server(n) == nil {
				st.Pending++
			}
		}
		for n := range a.Skills {
			if l.skill(n) == nil {
				st.Pending++
			}
		}
		_ = id
	}
	st.Found = found(l, ts, holds)
	return st
}

func mcpCell(l *Library, t *Target, h held, s *Server) *Cell {
	c := &Cell{Want: slices.Contains(s.Agents, t.ID)}
	switch {
	case t.MCP == nil:
		c.State, c.Reason = CellUnsupported, "没有 MCP 配置位置"
		return c
	case h.mcpErr != nil:
		c.State, c.Reason = CellError, h.mcpErr.Error()
		return c
	}
	if err := t.MCP.Supports(&s.Server); err != nil {
		c.State, c.Reason = CellUnsupported, err.Error()
		return c
	}
	wrote, mine := "", false
	if a := l.Applied[t.ID]; a != nil {
		wrote, mine = a.MCP[s.Name]
	}
	cur, there := h.mcp[s.Name]
	c.State = cellOf(c.Want, there, mine, there && t.MCP.Same(cur, &s.Server), there && cur.Hash() == wrote)
	return c
}

func skillCell(l *Library, t *Target, h held, s *Skill) *Cell {
	c := &Cell{Want: slices.Contains(s.Agents, t.ID)}
	switch {
	case t.Skills == "":
		c.State, c.Reason = CellUnsupported, "没有 skill 目录"
		if t.SkillsVia != "" {
			c.Reason = "读共享目录 ~/.agents/skills"
		}
		return c
	case h.sklErr != nil:
		c.State, c.Reason = CellError, h.sklErr.Error()
		return c
	}
	wrote, mine := "", false
	if a := l.Applied[t.ID]; a != nil {
		wrote, mine = a.Skills[s.Name]
	}
	cur, there := h.skills[s.Name]
	if there && cur.Link {
		c.State, c.Reason = CellLink, "这里是其他工具建的链接"
		return c
	}
	c.State = cellOf(c.Want, there, mine, there && cur.Hash == s.Hash, there && cur.Hash == wrote)
	return c
}

// cellOf is an item's state in a target: ticked or not (want), there or
// not, entered in Applied (mine), there as the library has it (same), there
// as the library last wrote it (asWritten).
func cellOf(want, there, mine, same, asWritten bool) string {
	switch {
	case want && !there:
		return CellAdd
	case want && same:
		return CellOn
	case want && mine && asWritten:
		return CellUpdate
	case want && mine:
		return CellDrift
	case want:
		return CellForeign
	case !there:
		return CellOff
	case mine && (same || asWritten):
		return CellRemove
	case mine:
		return CellDrift
	default:
		// the agent's own, not the library's: not ticked here, not touched
		return CellOff
	}
}

// found lists what the targets hold that the library has nothing by that
// name of (or has, but not from that target), grouped by version.
func found(l *Library, ts []*Target, holds map[string]held) []*Found {
	groups := map[string]*Found{}
	names := map[string]map[string]bool{} // kind/name → hashes
	add := func(f *Found, target string) {
		key := f.Kind + "\x00" + f.Name + "\x00" + f.Hash
		g := groups[key]
		if g == nil {
			g = f
			groups[key] = g
		}
		if !slices.Contains(g.In, target) {
			g.In = append(g.In, target)
		}
		k := f.Kind + "\x00" + f.Name
		if names[k] == nil {
			names[k] = map[string]bool{}
		}
		names[k][f.Hash] = true
	}
	for _, t := range ts {
		h := holds[t.ID]
		a := l.Applied[t.ID]
		for name, s := range h.mcp {
			if a != nil {
				if _, ok := a.MCP[name]; ok {
					continue
				}
			}
			if lib := l.server(name); lib != nil && t.MCP.Same(s, &lib.Server) {
				continue // the library's, there without the library writing it
			}
			srv := *s
			add(&Found{Kind: KindMCP, Name: name, Hash: s.Hash(), Server: &srv}, t.ID)
		}
		for name, s := range h.skills {
			if a != nil {
				if _, ok := a.Skills[name]; ok {
					continue
				}
			}
			if lib := l.skill(name); lib != nil && lib.Hash == s.Hash {
				continue
			}
			add(&Found{Kind: KindSkill, Name: name, Description: s.Description, Hash: s.Hash, Path: s.Path}, t.ID)
		}
	}
	out := make([]*Found, 0, len(groups))
	for _, g := range groups {
		inLib := g.Kind == KindMCP && l.server(g.Name) != nil || g.Kind == KindSkill && l.skill(g.Name) != nil
		g.Conflict = inLib || len(names[g.Kind+"\x00"+g.Name]) > 1
		sort.Strings(g.In)
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return len(out[i].In) > len(out[j].In)
	})
	return out
}

// ---- import --------------------------------------------------------------

// ImportReq picks one found version to import, under its own name or As.
type ImportReq struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Hash string `json:"hash"`
	As   string `json:"as,omitempty"`
}

// Import takes found items into the library. Imported under its own name,
// an item is entered as given to the targets it was found in, so unticking
// one takes it out of it; under another name (As) it is given to none yet.
// An empty list imports every found item that has no conflict.
func (m *Manager) Import(reqs []ImportReq) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, err := m.load()
	if err != nil {
		return err
	}
	st := m.state(l)
	if len(reqs) == 0 {
		for _, f := range st.Found {
			if !f.Conflict {
				reqs = append(reqs, ImportReq{Kind: f.Kind, Name: f.Name, Hash: f.Hash})
			}
		}
	}
	b, err := newBackup(m.backups(), "导入", m.now())
	if err != nil {
		return err
	}
	if err := b.save(m.file()); err != nil {
		return err
	}
	var errs []error
	for _, r := range reqs {
		if err := m.importOne(l, st.Found, r, b); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.Name, err))
		}
	}
	if err := m.save(l); err != nil {
		errs = append(errs, err)
	}
	errs = append(errs, b.done())
	pruneBackups(m.backups(), m.now())
	return errors.Join(errs...)
}

func (m *Manager) importOne(l *Library, found []*Found, r ImportReq, b *backup) error {
	var f *Found
	for _, x := range found {
		if x.Kind == r.Kind && x.Name == r.Name && x.Hash == r.Hash {
			f = x
		}
	}
	if f == nil {
		return errors.New("没有找到这一项（可能已经变了，请刷新）")
	}
	name := r.Name
	if r.As != "" {
		name = r.As
	}
	if err := CheckName(name); err != nil {
		return err
	}
	renamed := name != f.Name
	switch f.Kind {
	case KindMCP:
		if l.server(name) != nil {
			return fmt.Errorf("库里已经有叫 %s 的 MCP server", name)
		}
		s := &Server{Server: *f.Server}
		s.Name = name
		if !renamed {
			s.Agents = slices.Clone(f.In)
			for _, t := range f.In {
				l.applied(t).MCP[name] = f.Hash
			}
		}
		l.MCP = append(l.MCP, s)
	case KindSkill:
		if l.skill(name) != nil {
			return fmt.Errorf("库里已经有叫 %s 的 skill", name)
		}
		dst := filepath.Join(m.store(), name)
		if err := b.save(dst); err != nil {
			return err
		}
		if err := putDir(f.Path, dst); err != nil {
			return err
		}
		s := &Skill{Name: name, Description: f.Description, Hash: f.Hash}
		if !renamed {
			s.Agents = slices.Clone(f.In)
			for _, t := range f.In {
				l.applied(t).Skills[name] = f.Hash
			}
		}
		l.Skills = append(l.Skills, s)
	default:
		return fmt.Errorf("不认识的类型 %q", f.Kind)
	}
	return nil
}

// ---- editing ---------------------------------------------------------------

// PutServer adds a server to the library, or replaces the one named old
// (renaming it when the names differ: the old name is taken out of the
// targets at the next apply, the new one written). agents are the targets
// to give it to.
func (m *Manager) PutServer(old string, s mcpfmt.Server, agents []string) error {
	if err := CheckName(s.Name); err != nil {
		return err
	}
	if err := s.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	l, err := m.load()
	if err != nil {
		return err
	}
	if s.Name != old && l.server(s.Name) != nil {
		return fmt.Errorf("库里已经有叫 %s 的 MCP server", s.Name)
	}
	if old != "" {
		l.MCP = slices.DeleteFunc(l.MCP, func(x *Server) bool { return x.Name == old })
	}
	l.MCP = append(l.MCP, &Server{Server: s, Agents: m.known(l, agents)})
	return m.save(l)
}

// PutSkill copies the skill folder at path (holding a SKILL.md) into the
// library as name, replacing a library skill by that name.
func (m *Manager) PutSkill(name, path string, agents []string) error {
	if err := CheckName(name); err != nil {
		return err
	}
	if skillFile(path) == "" {
		return fmt.Errorf("%s 里没有 SKILL.md", path)
	}
	h, err := hashDir(path)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	l, err := m.load()
	if err != nil {
		return err
	}
	dst := filepath.Join(m.store(), name)
	if filepath.Clean(path) != filepath.Clean(dst) {
		b, err := newBackup(m.backups(), "添加 skill "+name, m.now())
		if err != nil {
			return err
		}
		if err := b.save(dst); err != nil {
			return err
		}
		if err := putDir(path, dst); err != nil {
			return err
		}
		if err := b.done(); err != nil {
			return err
		}
	}
	_, desc := frontmatter(skillFile(dst))
	l.Skills = slices.DeleteFunc(l.Skills, func(x *Skill) bool { return x.Name == name })
	l.Skills = append(l.Skills, &Skill{Name: name, Description: desc, Hash: h, Agents: m.known(l, agents)})
	return m.save(l)
}

// SetAgents sets the targets an item is given to; the next apply writes
// and takes out accordingly.
func (m *Manager) SetAgents(kind, name string, agents []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, err := m.load()
	if err != nil {
		return err
	}
	switch kind {
	case KindMCP:
		s := l.server(name)
		if s == nil {
			return fmt.Errorf("库里没有 MCP server %s", name)
		}
		s.Agents = m.known(l, agents)
	case KindSkill:
		s := l.skill(name)
		if s == nil {
			return fmt.Errorf("库里没有 skill %s", name)
		}
		s.Agents = m.known(l, agents)
	default:
		return fmt.Errorf("不认识的类型 %q", kind)
	}
	return m.save(l)
}

// known keeps the ids of targets that exist.
func (m *Manager) known(l *Library, ids []string) []string {
	have := map[string]bool{}
	for _, t := range m.targets(l) {
		have[t.ID] = true
	}
	var out []string
	for _, id := range ids {
		if have[id] && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// Adopt takes an agent's own version of an item into the library: the
// target's copy (one that drifted, or a foreign one by that name) becomes
// the library's, given to that target as it is.
func (m *Manager) Adopt(kind, name, target string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, err := m.load()
	if err != nil {
		return err
	}
	var t *Target
	for _, x := range m.targets(l) {
		if x.ID == target {
			t = x
		}
	}
	if t == nil {
		return fmt.Errorf("没有位置 %s", target)
	}
	h := readTarget(t)
	switch kind {
	case KindMCP:
		cur, ok := h.mcp[name]
		s := l.server(name)
		if !ok || s == nil {
			return fmt.Errorf("%s 里没有 %s", t.Label, name)
		}
		s.Server = *cur
		s.Name = name
		if !slices.Contains(s.Agents, target) {
			s.Agents = append(s.Agents, target)
		}
		l.applied(target).MCP[name] = cur.Hash()
	case KindSkill:
		cur, ok := h.skills[name]
		s := l.skill(name)
		if !ok || s == nil {
			return fmt.Errorf("%s 里没有 %s", t.Label, name)
		}
		b, err := newBackup(m.backups(), "采用 "+t.Label+" 的 "+name, m.now())
		if err != nil {
			return err
		}
		dst := filepath.Join(m.store(), name)
		if err := b.save(dst); err != nil {
			return err
		}
		if err := b.save(m.file()); err != nil {
			return err
		}
		if err := putDir(cur.Path, dst); err != nil {
			return err
		}
		if err := b.done(); err != nil {
			return err
		}
		s.Hash, s.Description = cur.Hash, cur.Description
		if !slices.Contains(s.Agents, target) {
			s.Agents = append(s.Agents, target)
		}
		l.applied(target).Skills[name] = cur.Hash
	default:
		return fmt.Errorf("不认识的类型 %q", kind)
	}
	return m.save(l)
}

// ---- apply -----------------------------------------------------------------

// Result is what an apply did with one item in one target.
type Result struct {
	Target string `json:"target"`
	Label  string `json:"label"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Action string `json:"action"` // written, removed, skipped, failed
	Reason string `json:"reason,omitempty"`
}

// ApplyOptions: Overwrite lists "kind/name/target" cells whose drifted or
// foreign entry may be replaced or taken out. Only limits the apply to the
// targets listed (all when empty).
type ApplyOptions struct {
	Overwrite []string `json:"overwrite,omitempty"`
	Only      []string `json:"only,omitempty"`
}

// Apply writes each ticked item to its targets and takes out each one the
// library wrote that is no longer ticked or no longer in the library. The
// files and folders it changes are backed up first.
func (m *Manager) Apply(opt ApplyOptions) ([]Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, err := m.load()
	if err != nil {
		return nil, err
	}
	return m.apply(l, opt, "应用")
}

func (m *Manager) apply(l *Library, opt ApplyOptions, what string) ([]Result, error) {
	b, err := newBackup(m.backups(), what, m.now())
	if err != nil {
		return nil, err
	}
	if err := b.save(m.file()); err != nil {
		return nil, err
	}
	over := map[string]bool{}
	for _, k := range opt.Overwrite {
		over[k] = true
	}
	var res []Result
	for _, t := range m.targets(l) {
		if len(opt.Only) > 0 && !slices.Contains(opt.Only, t.ID) {
			continue
		}
		res = append(res, m.applyMCP(l, t, b, over)...)
		res = append(res, m.applySkills(l, t, b, over)...)
	}
	errs := []error{m.save(l), b.done()}
	pruneBackups(m.backups(), m.now())
	return res, errors.Join(errs...)
}

func (m *Manager) applyMCP(l *Library, t *Target, b *backup, over map[string]bool) []Result {
	var res []Result
	r := func(name, action, reason string) {
		res = append(res, Result{Target: t.ID, Label: t.Label, Kind: KindMCP, Name: name, Action: action, Reason: reason})
	}
	a := l.Applied[t.ID]
	var wants []*Server
	for _, s := range l.MCP {
		if slices.Contains(s.Agents, t.ID) {
			wants = append(wants, s)
		}
	}
	if t.MCP == nil {
		for _, s := range wants {
			r(s.Name, "skipped", "没有 MCP 配置位置")
		}
		return res
	}
	if len(wants) == 0 && (a == nil || len(a.MCP) == 0) {
		return nil
	}
	cur, err := t.MCP.Read()
	if err != nil {
		for _, s := range wants {
			r(s.Name, "failed", "文件无法读取，未修改："+err.Error())
		}
		return res
	}
	app := l.applied(t.ID)
	var puts []*mcpfmt.Server
	var putNames []string
	for _, s := range wants {
		key := KindMCP + "/" + s.Name + "/" + t.ID
		if err := t.MCP.Supports(&s.Server); err != nil {
			r(s.Name, "skipped", err.Error())
			continue
		}
		c, there := cur[s.Name]
		wrote, mine := app.MCP[s.Name]
		switch {
		case there && t.MCP.Same(c, &s.Server):
			app.MCP[s.Name] = c.Hash() // there already: the library's now
		case !there || mine && c.Hash() == wrote || over[key]:
			srv := s.Server
			puts = append(puts, &srv)
			putNames = append(putNames, s.Name)
		case mine:
			r(s.Name, "skipped", "在 "+t.Label+" 里被改过，未覆盖")
		default:
			r(s.Name, "skipped", t.Label+" 里已有同名的 server，未覆盖")
		}
	}
	var removes []string
	for name, wrote := range app.MCP {
		if s := l.server(name); s != nil && slices.Contains(s.Agents, t.ID) {
			continue
		}
		c, there := cur[name]
		switch {
		case !there:
			delete(app.MCP, name)
		case c.Hash() == wrote || over[KindMCP+"/"+name+"/"+t.ID]:
			removes = append(removes, name)
		default:
			r(name, "skipped", "在 "+t.Label+" 里被改过，未移除")
		}
	}
	if len(puts) == 0 && len(removes) == 0 {
		return res
	}
	if err := b.save(t.MCP.Path); err != nil {
		for _, n := range append(putNames, removes...) {
			r(n, "failed", err.Error())
		}
		return res
	}
	if len(puts) > 0 {
		if err := t.MCP.Put(puts...); err != nil {
			for _, n := range putNames {
				r(n, "failed", err.Error())
			}
		} else {
			// what is there as written (a format may read a server back in
			// its own terms: OpenCode's SSE as http) is what drift is told by
			back, _ := t.MCP.Read()
			for _, s := range puts {
				app.MCP[s.Name] = s.Hash()
				if b := back[s.Name]; b != nil {
					app.MCP[s.Name] = b.Hash()
				}
				r(s.Name, "written", "")
			}
		}
	}
	if len(removes) > 0 {
		if err := t.MCP.Remove(removes...); err != nil {
			for _, n := range removes {
				r(n, "failed", err.Error())
			}
		} else {
			for _, n := range removes {
				delete(app.MCP, n)
				r(n, "removed", "")
			}
		}
	}
	return res
}

func (m *Manager) applySkills(l *Library, t *Target, b *backup, over map[string]bool) []Result {
	var res []Result
	r := func(name, action, reason string) {
		res = append(res, Result{Target: t.ID, Label: t.Label, Kind: KindSkill, Name: name, Action: action, Reason: reason})
	}
	a := l.Applied[t.ID]
	var wants []*Skill
	for _, s := range l.Skills {
		if slices.Contains(s.Agents, t.ID) {
			wants = append(wants, s)
		}
	}
	if t.Skills == "" {
		for _, s := range wants {
			r(s.Name, "skipped", "没有 skill 目录")
		}
		return res
	}
	if len(wants) == 0 && (a == nil || len(a.Skills) == 0) {
		return nil
	}
	list, err := scanSkills(t.Skills)
	if err != nil {
		for _, s := range wants {
			r(s.Name, "failed", err.Error())
		}
		return res
	}
	cur := map[string]FoundSkill{}
	for _, s := range list {
		cur[s.Name] = s
	}
	app := l.applied(t.ID)
	for _, s := range wants {
		key := KindSkill + "/" + s.Name + "/" + t.ID
		c, there := cur[s.Name]
		wrote, mine := app.Skills[s.Name]
		dst := filepath.Join(t.Skills, s.Name)
		switch {
		case there && c.Link:
			r(s.Name, "skipped", "这里是其他工具建的链接，未覆盖")
			continue
		case there && c.Hash == s.Hash:
			app.Skills[s.Name] = s.Hash
			continue
		case !there && exists(dst):
			r(s.Name, "skipped", dst+" 已存在但不是 skill，未覆盖")
			continue
		case !there || mine && c.Hash == wrote || over[key]:
		case mine:
			r(s.Name, "skipped", "在 "+t.Label+" 里被改过，未覆盖")
			continue
		default:
			r(s.Name, "skipped", t.Label+" 里已有同名的 skill，未覆盖")
			continue
		}
		if err := b.save(dst); err != nil {
			r(s.Name, "failed", err.Error())
			continue
		}
		if err := putDir(filepath.Join(m.store(), s.Name), dst); err != nil {
			r(s.Name, "failed", err.Error())
			continue
		}
		app.Skills[s.Name] = s.Hash
		r(s.Name, "written", "")
	}
	for name, wrote := range app.Skills {
		if s := l.skill(name); s != nil && slices.Contains(s.Agents, t.ID) {
			continue
		}
		c, there := cur[name]
		dst := filepath.Join(t.Skills, name)
		switch {
		case !there:
			delete(app.Skills, name)
			continue
		case c.Link:
			delete(app.Skills, name)
			continue
		case c.Hash != wrote && !over[KindSkill+"/"+name+"/"+t.ID]:
			r(name, "skipped", "在 "+t.Label+" 里被改过，未移除")
			continue
		}
		if err := b.save(dst); err != nil {
			r(name, "failed", err.Error())
			continue
		}
		if err := removeDir(dst); err != nil {
			r(name, "failed", err.Error())
			continue
		}
		delete(app.Skills, name)
		r(name, "removed", "")
	}
	return res
}

// ---- delete, projects, backups ---------------------------------------------

// Delete takes an item out of the library and, at once, out of every
// target the library gave it to (an apply limited to those).
func (m *Manager) Delete(kind, name string) ([]Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, err := m.load()
	if err != nil {
		return nil, err
	}
	var only []string
	for id, a := range l.Applied {
		_, inM := a.MCP[name]
		_, inS := a.Skills[name]
		if kind == KindMCP && inM || kind == KindSkill && inS {
			only = append(only, id)
		}
	}
	b, err := newBackup(m.backups(), "删除 "+name, m.now())
	if err != nil {
		return nil, err
	}
	switch kind {
	case KindMCP:
		if l.server(name) == nil {
			return nil, fmt.Errorf("库里没有 MCP server %s", name)
		}
		l.MCP = slices.DeleteFunc(l.MCP, func(s *Server) bool { return s.Name == name })
	case KindSkill:
		if l.skill(name) == nil {
			return nil, fmt.Errorf("库里没有 skill %s", name)
		}
		l.Skills = slices.DeleteFunc(l.Skills, func(s *Skill) bool { return s.Name == name })
		dst := filepath.Join(m.store(), name)
		if err := b.save(dst); err != nil {
			return nil, err
		}
		defer removeDir(dst)
	default:
		return nil, fmt.Errorf("不认识的类型 %q", kind)
	}
	if err := b.done(); err != nil {
		return nil, err
	}
	if len(only) == 0 {
		return nil, m.save(l)
	}
	return m.apply(l, ApplyOptions{Only: only}, "删除 "+name)
}

// AddProject adds a project folder, whose agents' project places become
// targets.
func (m *Manager) AddProject(dir string) error {
	dir = filepath.Clean(dir)
	if !filepath.IsAbs(dir) || !isDir(dir) {
		return fmt.Errorf("%s 不是一个文件夹", dir)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	l, err := m.load()
	if err != nil {
		return err
	}
	if !slices.Contains(l.Projects, dir) {
		l.Projects = append(l.Projects, dir)
		sort.Strings(l.Projects)
	}
	return m.save(l)
}

// RemoveProject takes the library's items out of the project's places
// (unticking them there, then an apply limited to them) and forgets it.
func (m *Manager) RemoveProject(dir string) ([]Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, err := m.load()
	if err != nil {
		return nil, err
	}
	if !slices.Contains(l.Projects, dir) {
		return nil, fmt.Errorf("没有项目 %s", dir)
	}
	prefix := ProjectTargetID(dir, "")
	inProject := func(id string) bool { return strings.HasPrefix(id, prefix) }
	var only []string
	for _, t := range ProjectTargets(dir, m.users()) {
		only = append(only, t.ID)
	}
	for _, s := range l.MCP {
		s.Agents = slices.DeleteFunc(s.Agents, inProject)
	}
	for _, s := range l.Skills {
		s.Agents = slices.DeleteFunc(s.Agents, inProject)
	}
	res, err := m.apply(l, ApplyOptions{Only: only}, "移除项目 "+filepath.Base(dir))
	// forgotten once nothing of the library's is left in it; what an
	// agent's change kept there stays listed until it is dealt with
	left := false
	for id, a := range l.Applied {
		if inProject(id) && (len(a.MCP) > 0 || len(a.Skills) > 0) {
			left = true
		}
	}
	if !left {
		l.Projects = slices.DeleteFunc(l.Projects, func(p string) bool { return p == dir })
		err = errors.Join(err, m.save(l))
	}
	return res, err
}

// Backups are the backups, newest first.
func (m *Manager) Backups() ([]Backup, error) { return listBackups(m.backups()) }

// Restore puts back every file and folder (library.json among them) as
// backup id kept it.
func (m *Manager) Restore(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return restoreBackup(m.backups(), id, m.now())
}

// Targets are the user's targets and each project's.
func (m *Manager) Targets() ([]*Target, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, err := m.load()
	if err != nil {
		return nil, err
	}
	return m.targets(l), nil
}
