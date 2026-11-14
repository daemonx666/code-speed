// Package hermes reads Hermes Agent's logs (Agent Speed spec/METRICS.md
// §4.15, MIT). Hermes keeps no transcript with usage; its logs/agent.log has
// one line per model API call with that call's tokens and duration. The log
// rotates by renaming files, and every log folder shares one parser state,
// so the source is polled: files are followed by identity, not by path.
//
// Supported: the log format of Hermes Agent up to v0.22.5 (main 9b904dcc22c2).
// Not read: state.db and the effort and speed settings of config.yaml (the
// usage model has no field for them); only the Codex runtime key of
// config.yaml is read.
package hermes

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/usage"
)

// Hermes is the source id.
const Hermes usage.Source = "hermes"

func init() {
	source.Register(source.Spec{
		Source: Hermes,
		Label:  "Hermes Agent",
		Color:  "#a3a3a3",
		Detect: func(home string) bool { return len(logFolders(homeDir(home))) > 0 },
		Poller: func() source.Poller { return &poller{} },
		Every:  2 * time.Second,
	})
}

// ---- roots -----------------------------------------------------------

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func foldEq(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// within reports whether p is root or lies inside it.
func within(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		if foldEq(p, root) {
			return true
		}
		// filepath.Rel is case-insensitive on Windows already
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

// defaultHome is the Hermes home without $HERMES_HOME.
func defaultHome(home string) string {
	if runtime.GOOS == "windows" {
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			return filepath.Join(v, "hermes")
		}
		return filepath.Join(home, "AppData", "Local", "hermes")
	}
	return filepath.Join(home, ".hermes")
}

// homeDir resolves the Hermes home (4.15 Roots): $HERMES_HOME is a profile
// or a home of its own.
func homeDir(home string) string {
	def := defaultHome(home)
	v := strings.TrimSpace(os.Getenv("HERMES_HOME"))
	if v == "" {
		return def
	}
	if v == "~" {
		v = home
	} else if strings.HasPrefix(v, "~/") || strings.HasPrefix(v, `~\`) {
		v = filepath.Join(home, v[2:])
	}
	if !filepath.IsAbs(v) {
		return def
	}
	v = filepath.Clean(v)
	if within(v, def) {
		return def
	}
	if foldEq(filepath.Base(filepath.Dir(v)), "profiles") {
		return filepath.Dir(filepath.Dir(v))
	}
	return v
}

// logFolders lists the existing log folders of a home: the default profile's,
// then every profile's in ordinal order.
func logFolders(h string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if !seen[p] && isDir(p) {
			seen[p] = true
			out = append(out, p)
		}
	}
	add(filepath.Join(h, "logs"))
	ents, _ := os.ReadDir(filepath.Join(h, "profiles"))
	var names []string
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, ".") || e.Type()&os.ModeSymlink != 0 || !e.IsDir() {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		add(filepath.Join(h, "profiles", n, "logs"))
	}
	return out
}

func isLogName(n string) bool {
	if n == "agent.log" {
		return true
	}
	d, ok := strings.CutPrefix(n, "agent.log.")
	if !ok || d == "" {
		return false
	}
	for i := 0; i < len(d); i++ {
		if d[i] < '0' || d[i] > '9' {
			return false
		}
	}
	return true
}

// ---- time ------------------------------------------------------------

func digits(b []byte) (int, bool) {
	n := 0
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

func daysIn(y, m int) int {
	switch m {
	case 2:
		if y%4 == 0 && (y%100 != 0 || y%400 == 0) {
			return 29
		}
		return 28
	case 4, 6, 9, 11:
		return 30
	}
	return 31
}

// parseStamp reads the 23-character timestamp "YYYY-MM-DD HH:MM:SS,mmm" of
// a record line as local wall time, in loc, like Python's naive
// datetime.timestamp() with fold 0.
func parseStamp(b []byte, loc *time.Location) (int64, bool) {
	if len(b) < 23 || b[4] != '-' || b[7] != '-' || b[10] != ' ' || b[13] != ':' || b[16] != ':' || b[19] != ',' {
		return 0, false
	}
	var v [7]int
	for i, r := range [7][2]int{{0, 4}, {5, 7}, {8, 10}, {11, 13}, {14, 16}, {17, 19}, {20, 23}} {
		n, ok := digits(b[r[0]:r[1]])
		if !ok {
			return 0, false
		}
		v[i] = n
	}
	y, mo, d, h, mi, s, msec := v[0], v[1], v[2], v[3], v[4], v[5], v[6]
	if y < 1970 || mo < 1 || mo > 12 || d < 1 || d > daysIn(y, mo) || h > 23 || mi > 59 || s > 59 {
		return 0, false
	}
	naive := time.Date(y, time.Month(mo), d, h, mi, s, 0, time.UTC).Unix()
	off := func(unix int64) int64 {
		_, o := time.Unix(unix, 0).In(loc).Zone()
		return int64(o)
	}
	before, after := off(naive-86400), off(naive+86400)
	best, found := int64(0), false
	for _, o := range [2]int64{before, after} {
		inst := naive - o
		if off(inst) == o && (!found || inst < best) {
			best, found = inst, true
		}
	}
	if !found { // skipped by a clock change: the offset before it
		best = naive - before
	}
	return best*1000 + int64(msec), true
}

// ---- lines -----------------------------------------------------------

var (
	turnStartRe = regexp.MustCompile(`(?s)^conversation turn: session=[^ ]* model=(.*?) provider=[^ ]* platform=([^ ]*) history=-?[0-9]+ msg=`)
	apiCallRe   = regexp.MustCompile(`(?s)^API call #([0-9]+): model=(.*?) provider=([^ ]*) in=(-?[0-9]+|\?) out=(-?[0-9]+|\?) total=(-?[0-9]+|\?) latency=(-?[0-9]+(?:\.[0-9]+)?)s(?: cache=(-?[0-9]+)/-?[0-9]+ \(-?[0-9]+%\))?(?: write=(-?[0-9]+))?(?: .*)?$`)
	failedRe    = regexp.MustCompile(`(?s)^(?:\[[^\]]*\])? *(?:API call failed after |Non-retryable client error: )`)
)

const (
	loggerTurn = "agent.turn_context"
	loggerLoop = "agent.conversation_loop"
)

type kind int

const (
	kNone kind = iota
	kTurnStart
	kAPI
	kTurnEnd
	kFailed
)

// split reads a record line: its timestamp text, level, session id, logger
// and message.
func split(line []byte) (stamp []byte, level, sid, logger, msg string, ok bool) {
	if len(line) < 25 || line[23] != ' ' {
		return
	}
	stamp = line[:23]
	rest := string(line[24:])
	sp := strings.IndexByte(rest, ' ')
	if sp <= 0 {
		return
	}
	level, rest = rest[:sp], rest[sp:] // rest starts with the space
	switch level {
	case "DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL":
	default:
		return
	}
	for _, lg := range [2]string{loggerTurn, loggerLoop} {
		if m, found := strings.CutPrefix(rest, " "+lg+": "); found {
			return stamp, level, "", lg, m, true
		}
	}
	if !strings.HasPrefix(rest, " [") {
		return
	}
	best, bestLg := -1, ""
	for _, lg := range [2]string{loggerTurn, loggerLoop} {
		if i := strings.Index(rest, "] "+lg+": "); i >= 2 && (best < 0 || i < best) {
			best, bestLg = i, lg
		}
	}
	if best < 0 || best == 2 { // empty id
		return
	}
	return stamp, level, rest[2:best], bestLg, rest[best+len("] "+bestLg+": "):], true
}

func capTok(s string) int64 {
	if s == "?" || strings.HasPrefix(s, "-") {
		return 0
	}
	var n int64
	for i := 0; i < len(s); i++ {
		n = n*10 + int64(s[i]-'0')
		if n >= source.TokCap {
			return source.TokCap
		}
	}
	return n
}

// latencyMs is the latency in whole milliseconds, truncated, from its
// decimal digits; neg when it starts with a minus sign.
func latencyMs(s string) (ms int64, neg bool) {
	if strings.HasPrefix(s, "-") {
		return 0, true
	}
	ip, fp, _ := strings.Cut(s, ".")
	var n int64
	for i := 0; i < len(ip); i++ {
		n = n*10 + int64(ip[i]-'0')
		if n >= 1e12 {
			return 1e15, false
		}
	}
	n *= 1000
	fp = (fp + "000")[:3]
	f, _ := strconv.Atoi(fp)
	return n + int64(f), false
}

// ---- agents and log sets --------------------------------------------

type agent struct {
	id       string
	phase    usage.Phase
	inFlight int64
	model    string
	sub      bool
	lastLine int64
	context  int64
}

func (a *agent) state(set string) usage.AgentState {
	return usage.AgentState{Agent: a.id, Source: Hermes, Model: a.model, Subagent: a.sub,
		Phase: a.phase, InFlightMs: a.inFlight, LastLineMs: a.lastLine, Context: a.context}
}

type logSet struct {
	dir    string
	agents map[string]*agent
	dirty  map[string]bool
	codex  bool

	cfgSize  int64
	cfgMtime time.Time
	cfgRead  bool
}

func newSet(dir string) *logSet {
	return &logSet{dir: dir, agents: map[string]*agent{}, dirty: map[string]bool{}}
}

func (s *logSet) agent(sid string) *agent {
	a := s.agents[sid]
	if a == nil {
		a = &agent{id: s.dir + "#" + sid, phase: usage.Idle}
		s.agents[sid] = a
	}
	return a
}

type poller struct {
	// set by tests
	dirs func() []string
	loc  *time.Location

	started bool
	sets    map[string]*logSet
	files   map[fileID]*fileState
	d       *source.Dedup
	sink    source.Sink
}

type fileState struct {
	set    *logSet
	offset int64
	skip   bool // drop bytes up to the first newline
}

func (p *poller) location() *time.Location {
	if p.loc != nil {
		return p.loc
	}
	return time.Local
}

func (p *poller) logDirs() []string {
	if p.dirs != nil {
		return p.dirs()
	}
	h, err := os.UserHomeDir()
	if err != nil {
		h = ""
	}
	return logFolders(homeDir(h))
}

func (s *logSet) line(p *poller, b []byte) {
	stamp, level, sid, logger, msg, ok := split(b)
	if !ok {
		return
	}
	var k kind
	switch {
	case logger == loggerTurn && level == "INFO" && strings.HasPrefix(msg, "conversation turn: "):
		k = kTurnStart
	case logger == loggerLoop && level == "INFO" && strings.HasPrefix(msg, "API call #"):
		k = kAPI
	case logger == loggerLoop && (level == "INFO" || level == "WARNING") &&
		(strings.HasPrefix(msg, "Turn ended: ") || strings.HasPrefix(msg, "Turn ended with pending tool result")):
		k = kTurnEnd
	case logger == loggerLoop && level == "ERROR" && failedRe.MatchString(msg):
		k = kFailed
	}
	if k == kNone {
		return
	}
	var m []string
	switch k {
	case kTurnStart:
		if m = turnStartRe.FindStringSubmatch(msg); m == nil {
			return
		}
	case kAPI:
		if m = apiCallRe.FindStringSubmatch(msg); m == nil {
			return // malformed
		}
	}
	ts, ok := parseStamp(stamp, p.location())
	if !ok {
		return
	}
	if !utf8.ValidString(sid) {
		sid = strings.ToValidUTF8(sid, "�")
	}
	a := s.agent(sid)
	s.dirty[sid] = true
	if ts > a.lastLine {
		a.lastLine = ts
	}
	switch k {
	case kTurnStart:
		if s.codex {
			a.phase, a.inFlight = usage.Idle, 0
		} else {
			a.phase, a.inFlight = usage.Generating, ts
		}
		if m[1] != "" {
			a.model = strings.Clone(m[1])
		}
		a.sub = m[2] == "subagent"
	case kTurnEnd, kFailed:
		a.phase, a.inFlight = usage.Idle, 0
	case kAPI:
		s.apiCall(p, a, sid, string(stamp), ts, m)
	}
}

func (s *logSet) apiCall(p *poller, a *agent, sid, stampText string, ts int64, m []string) {
	key := "hermes:" + sid + ":" + m[1] + ":" + stampText
	if p.d != nil {
		if _, seen := p.d.Claim(key, s.dir); seen {
			return
		}
	}
	model := m[2]
	if model != "" {
		a.model = strings.Clone(model)
	}
	if a.phase == usage.Generating {
		a.inFlight = ts
	}
	in, out := m[4], m[5]
	if in == "?" || out == "?" {
		return
	}
	tin, tout := capTok(in), capTok(out)
	if tin == 0 && tout == 0 {
		return
	}
	r := usage.Response{Key: key, Source: Hermes, Agent: a.id, Model: strings.Clone(model),
		EndMs: ts, Final: true}
	if prov := m[3]; prov != "" && prov != "unknown" {
		r.Provider = strings.Clone(prov)
	}
	if d, neg := latencyMs(m[7]); !neg {
		r.StartMs = ts - d
	}
	r.Input, r.Output = tin, tout
	if m[8] != "" {
		r.CachedInput = capTok(m[8])
	}
	if m[9] != "" {
		r.CacheWrite = capTok(m[9])
	}
	a.context = tin
	p.sink.Response(r)
}

// ---- polling ---------------------------------------------------------

type fileRef struct {
	path  string
	id    fileID
	size  int64
	mtime time.Time
	set   *logSet
}

func (p *poller) Poll(ctx context.Context, since, now time.Time, d *source.Dedup, sink source.Sink) error {
	if p.sets == nil {
		p.sets = map[string]*logSet{}
		p.files = map[fileID]*fileState{}
	}
	p.d, p.sink = d, sink
	defer func() { p.d, p.sink = nil, nil }()

	var refs []fileRef
	for _, dir := range p.logDirs() {
		set := p.sets[dir]
		if set == nil {
			set = newSet(dir)
			p.sets[dir] = set
		}
		set.checkConfig()
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !isLogName(e.Name()) {
				continue
			}
			path := filepath.Join(dir, e.Name())
			fi, err := os.Lstat(path)
			if err != nil || !fi.Mode().IsRegular() {
				continue
			}
			id, ok := identity(path, fi)
			if !ok {
				continue
			}
			refs = append(refs, fileRef{path, id, fi.Size(), fi.ModTime(), set})
		}
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if !refs[i].mtime.Equal(refs[j].mtime) {
			return refs[i].mtime.Before(refs[j].mtime)
		}
		return refs[i].path < refs[j].path
	})
	cutoff := since.Add(-15 * time.Minute)
	for _, r := range refs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fs := p.files[r.id]
		if fs == nil {
			fs = &fileState{set: r.set}
			if !p.started && r.mtime.Before(cutoff) {
				fs.offset = r.size
				fs.skip = r.size > 0 && !endsWithNewline(r.path, r.size)
			}
			p.files[r.id] = fs
		}
		p.read(r.path, fs)
	}
	p.started = true
	for _, set := range p.sets {
		for sid := range set.dirty {
			sink.Agent(set.agents[sid].state(set.dir))
		}
		clear(set.dirty)
	}
	return nil
}

func endsWithNewline(path string, size int64) bool {
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()
	var b [1]byte
	if _, err := f.ReadAt(b[:], size-1); err != nil {
		return true
	}
	return b[0] == '\n'
}

// read feeds the complete lines of a file from its offset on.
func (p *poller) read(path string, fs *fileState) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return
	}
	if st.Size() < fs.offset { // shrunk: read again
		fs.offset, fs.skip = 0, false
	}
	if _, err := f.Seek(fs.offset, io.SeekStart); err != nil {
		return
	}
	br := bufio.NewReaderSize(f, 64<<10)
	for {
		b, err := br.ReadBytes('\n')
		if err != nil || len(b) == 0 || b[len(b)-1] != '\n' {
			return // an incomplete last line waits for its newline
		}
		fs.offset += int64(len(b))
		if fs.skip {
			fs.skip = false
			continue
		}
		b = b[:len(b)-1]
		if n := len(b); n > 0 && b[n-1] == '\r' {
			b = b[:n-1]
		}
		if len(b) > source.MaxLine {
			continue
		}
		fs.set.line(p, b)
	}
}
