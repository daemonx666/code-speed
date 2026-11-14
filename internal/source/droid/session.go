package droid

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/usage"
)

const (
	weightCap  = 8 << 20 // longest line weight, bytes
	systemSize = 64 << 10
)

var sessionID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// call is an assistant message waiting for its tokens.
type call struct {
	key     string
	startMs int64
	endMs   int64
	model   string // the message's modelId, "" for none
	wIn     int64
	wOut    int64
}

// fileSig identifies a file's state: it changed when any part does.
type fileSig struct {
	fi     os.FileInfo
	exists bool
}

func sigOf(path string) fileSig {
	fi, err := os.Lstat(path)
	if err != nil {
		return fileSig{}
	}
	return fileSig{fi, true}
}

func (a fileSig) same(b fileSig) bool {
	if a.exists != b.exists {
		return false
	}
	if !a.exists {
		return true
	}
	return os.SameFile(a.fi, b.fi) && a.fi.Size() == b.fi.Size() && a.fi.ModTime().Equal(b.fi.ModTime())
}

// session is one transcript and its sidecar.
type session struct {
	path    string
	id      string
	side    string
	dedup   *source.Dedup
	sink    source.Sink
	atEnd   bool // start at the transcript's end (older than the startup window)
	started bool

	cold      bool
	lastNewer time.Time

	// transcript reading
	fi        os.FileInfo
	off       int64
	seenSize  int64 // the transcript's size at the last cycle
	baseOff   int64
	compactAt int64 // start of the latest compaction_state line, else baseOff

	// transcript facts
	sawStart     bool
	project      string
	parent       string
	subagent     bool
	pendingStart int64
	hasPending   bool
	phase        usage.Phase
	inFlight     int64
	lastLine     int64
	lastModel    string // modelId of the latest call
	haveCall     bool
	seen         map[string]bool
	open         []*call

	// sidecar
	sessModel  string
	sideSig    fileSig
	sideRead   bool
	sideBad    bool // the last read found it unreadable
	ownUnknown bool // counters known but own counters not (parent unreadable)
	needH      bool // H is taken from the sidecar at the next good read
	H, P       counters
	parentBad  *fileSig

	reported  usage.AgentState
	didReport bool
}

func newSession(path string, d *source.Dedup, sink source.Sink) *session {
	s := &session{path: path, dedup: d, sink: sink, side: sidecarPath(path)}
	s.id = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	s.reset()
	return s
}

// reset starts the session over (a new session, or a transcript that
// shrank or changed identity). It keeps the remembered parent signature.
func (s *session) reset() {
	s.fi, s.off, s.baseOff, s.compactAt = nil, 0, 0, 0
	s.sawStart, s.project, s.parent, s.subagent = false, "", "", false
	s.pendingStart, s.hasPending = 0, false
	s.phase, s.inFlight, s.lastLine = usage.Idle, 0, 0
	s.lastModel, s.haveCall = "", false
	s.seen = map[string]bool{}
	s.open = nil
	s.sideRead, s.seenSize = false, 0
	s.sessModel, s.sideSig, s.sideBad, s.ownUnknown, s.needH = "", fileSig{}, false, false, false
	s.H, s.P = counters{}, counters{}
	s.started = false
}

// retry reports whether the sidecar must be read again whatever its
// signature says.
func (s *session) retry() bool { return s.sideBad || s.ownUnknown || s.needH }

// cycle runs one read cycle: new transcript lines, the sidecar, then the
// allocation of the growth to the open calls.
func (s *session) cycle() {
	fi, err := os.Lstat(s.path)
	if err != nil || !fi.Mode().IsRegular() {
		return
	}
	if s.fi != nil && (!os.SameFile(s.fi, fi) || fi.Size() < s.off) {
		s.reset()
	}
	sig := sigOf(s.side)
	if s.started && s.sideRead && fi.Size() == s.seenSize && fi.ModTime().Equal(s.fi.ModTime()) &&
		sig.same(s.sideSig) && !s.retry() {
		return
	}
	if !s.started {
		s.started = true
		if s.atEnd {
			s.off, s.baseOff, s.compactAt = fi.Size(), fi.Size(), fi.Size()
			s.needH = true
		}
	}
	s.fi, s.seenSize = fi, fi.Size()
	s.feed(fi.Size())
	s.readSide()
	s.allocate()
	s.report()
}

// feed reads the complete lines between off and size.
func (s *session) feed(size int64) {
	if size <= s.off {
		return
	}
	f, err := os.Open(s.path)
	if err != nil {
		return
	}
	defer f.Close()
	r := bufio.NewReaderSize(io.NewSectionReader(f, s.off, size-s.off), 256<<10)
	var buf []byte
	skipping := false
	lineStart := s.off
	pos := s.off
	for {
		chunk, err := r.ReadSlice('\n')
		pos += int64(len(chunk))
		if len(chunk) > 0 && !skipping {
			buf = append(buf, chunk...)
			if len(buf) > source.MaxLine {
				skipping, buf = true, nil
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil || len(chunk) == 0 || chunk[len(chunk)-1] != '\n' {
			break // an incomplete last line waits for its end
		}
		if !skipping {
			s.line(buf[:len(buf)-1], lineStart)
		}
		buf, skipping, lineStart = buf[:0], false, pos
	}
	s.off = lineStart
}

// line feeds one complete line (without its \n) that starts at offset off.
func (s *session) line(b []byte, off int64) {
	if !source.IsObject(b) || !gjson.ValidBytes(b) {
		return
	}
	var ts int64
	hasTS := false
	if v := gjson.GetBytes(b, "timestamp"); v.Type == gjson.String {
		ts, hasTS = parseTime(v.Str)
	}
	if hasTS && ts > s.lastLine {
		s.lastLine = ts
	}
	switch gjson.GetBytes(b, "type").String() {
	case "session_start":
		if s.sawStart {
			return
		}
		s.sawStart = true
		cwd := gjson.GetBytes(b, "cwd")
		if cwd.Type == gjson.String {
			s.project = projectOf(cwd.Str)
		}
		if v := gjson.GetBytes(b, "parent"); v.Type == gjson.String && sessionID.MatchString(v.Str) {
			s.parent = strings.Clone(v.Str)
		}
		if v := gjson.GetBytes(b, "callingSessionId"); v.Type == gjson.String && sessionID.MatchString(v.Str) {
			s.subagent = true
		}
	case "message":
		if !hasTS {
			return
		}
		switch gjson.GetBytes(b, "message.role").String() {
		case "user":
			if gjson.GetBytes(b, "message.visibility").String() == "user_only" {
				return
			}
			s.pendingStart, s.hasPending = ts, true
			s.phase, s.inFlight = usage.Generating, ts
		case "assistant":
			s.assistant(b, ts, off, int64(len(b)))
		}
	case "compaction_state":
		if !hasTS {
			return
		}
		if s.phase == usage.Generating {
			s.pendingStart, s.hasPending = ts, true
			s.inFlight = ts
		}
		s.compactAt = off
	case "agent_turn_outcome":
		s.phase, s.inFlight = usage.Idle, 0
		s.pendingStart, s.hasPending = 0, false
	}
}

func (s *session) assistant(b []byte, ts, off, length int64) {
	id := gjson.GetBytes(b, "id")
	if id.Type != gjson.String || id.Str == "" {
		return
	}
	key := "droid:" + id.Str
	if s.seen[key] {
		return
	}
	if mine, _ := s.dedup.Claim(key, s.path); !mine {
		return
	}
	s.seen[key] = true
	c := &call{key: key, endMs: ts, wOut: min(length, weightCap),
		wIn: min(systemSize+off-s.compactAt, weightCap)}
	if s.hasPending {
		c.startMs = s.pendingStart
	}
	if v := gjson.GetBytes(b, "message.modelId"); v.Type == gjson.String && strings.TrimSpace(v.Str) != "" {
		c.model = strings.Clone(v.Str)
	}
	s.haveCall = true
	s.lastModel = c.model
	s.open = append(s.open, c)
	s.pendingStart, s.hasPending = 0, false
	s.phase, s.inFlight = usage.Idle, 0
	if content := gjson.GetBytes(b, "message.content"); content.IsArray() {
		content.ForEach(func(_, blk gjson.Result) bool {
			if blk.Get("type").String() == "tool_use" {
				s.phase = usage.Tools
				return false
			}
			return true
		})
	}
}

// readSide reads the sidecar when it is new or changed, or its last read
// failed, and adds the growth of the own counters to P.
func (s *session) readSide() {
	sig := sigOf(s.side)
	if s.sideRead && sig.same(s.sideSig) && !s.retry() {
		return
	}
	s.sideRead = true
	sc, st := readSidecar(s.side)
	switch st {
	case sideUnreadable:
		s.sideBad = true
		s.sideSig = sig
		return
	case sideMissing:
		s.sideBad, s.ownUnknown = false, false
		s.sideSig = sig
		s.needH = false // no sidecar: H stays 0
		return
	}
	s.sideBad = false
	s.sideSig = sig
	s.sessModel = sc.model
	if !sc.has {
		s.ownUnknown = false
		s.needH = false
		return
	}
	own, known := s.own(sc.counters)
	if !known {
		s.ownUnknown = true
		return
	}
	s.ownUnknown = false
	if s.needH {
		s.needH = false
		s.H = own
		return
	}
	for f := range own {
		if g := own[f] - s.H[f]; g > 0 {
			s.P[f] = source.Sat(s.P[f], g)
		}
		s.H[f] = max(s.H[f], own[f])
	}
}

// own is the session's own counters: a session with a parent starts from
// the parent's counters.
func (s *session) own(c counters) (counters, bool) {
	if s.parent != "" {
		var pc counters
		ppath := filepath.Join(filepath.Dir(s.path), s.parent+".settings.json")
		psc, st := readSidecar(ppath)
		switch st {
		case sideOK:
			s.parentBad = nil
			if psc.has {
				pc = psc.counters
			}
		case sideUnreadable:
			sig := sigOf(ppath)
			if s.parentBad == nil || !s.parentBad.same(sig) {
				s.parentBad = &sig
				return c, false
			}
		}
		for f := range c {
			c[f] = max(0, c[f]-pc[f])
		}
	}
	c[cThink] = min(c[cThink], c[cOut])
	return c, true
}

// allocate gives the waiting growth to the open calls and emits them.
func (s *session) allocate() {
	if s.P == (counters{}) || len(s.open) == 0 {
		return
	}
	n := len(s.open)
	shares := make([]counters, n)
	for f := range s.P {
		w := make([]int64, n)
		for i, c := range s.open {
			if f == cOut || f == cThink {
				w[i] = c.wOut
			} else {
				w[i] = c.wIn
			}
		}
		for i, v := range split(s.P[f], w) {
			shares[i][f] = v
		}
	}
	for i, c := range s.open {
		sh := shares[i]
		model := c.model
		if model == "" {
			model = s.sessModel
		}
		r := usage.Response{
			Key: c.key, Source: Droid, Agent: s.path, Project: s.project,
			Model: model, StartMs: c.startMs, EndMs: c.endMs, Final: true,
			Tokens: usage.Tokens{
				Input:       source.Sat(source.Sat(sh[cIn], sh[cCacheWrite]), sh[cCacheRead]),
				CachedInput: sh[cCacheRead],
				CacheWrite:  sh[cCacheWrite],
				Output:      sh[cOut],
				Reasoning:   sh[cThink],
			},
		}
		if len(model) >= 7 && strings.EqualFold(model[:7], "custom:") {
			r.Provider = "custom"
		}
		s.sink.Response(r)
	}
	s.open = nil
	s.P = counters{}
}

// split divides total by weights with largest-remainder rounding in exact
// integers; leftover units go to the largest remainders, ties to the
// earlier call.
func split(total int64, w []int64) []int64 {
	out := make([]int64, len(w))
	var sum int64
	for _, v := range w {
		sum += v
	}
	if total <= 0 || sum <= 0 {
		return out
	}
	rem := make([]int64, len(w))
	var given int64
	for i, v := range w {
		out[i] = total * v / sum
		rem[i] = total * v % sum
		given += out[i]
	}
	idx := make([]int, len(w))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return rem[idx[a]] > rem[idx[b]] })
	for k := 0; k < int(total-given) && k < len(idx); k++ {
		out[idx[k]]++
	}
	return out
}

// state is the agent's current state.
func (s *session) state() usage.AgentState {
	model := s.sessModel
	if s.haveCall && s.lastModel != "" {
		model = s.lastModel
	}
	st := usage.AgentState{
		Agent: s.path, Source: Droid, Project: s.project, Model: model,
		Subagent: s.subagent, Phase: s.phase, LastLineMs: s.lastLine,
	}
	if s.phase == usage.Generating {
		st.InFlightMs = s.inFlight
	}
	return st
}

func (s *session) report() {
	st := s.state()
	if s.didReport && st == s.reported {
		return
	}
	s.reported, s.didReport = st, true
	s.sink.Agent(st)
}

// projectOf is the last component of a working directory, split on / and \.
func projectOf(cwd string) string {
	cwd = strings.TrimRight(cwd, `/\`)
	if i := strings.LastIndexAny(cwd, `/\`); i >= 0 {
		return strings.Clone(cwd[i+1:])
	}
	return strings.Clone(cwd)
}
