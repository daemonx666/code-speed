// Package antigravity reads Google Antigravity's conversation databases
// (Agent Speed spec/METRICS.md §4.8 and §5.4, MIT): one SQLite database per
// conversation, `conversations/<uuid>.db`, with protobuf blobs, written by
// agy 1.0.7 and later, the 2.x desktop app and the IDE 2.x. The older
// encrypted `.pb` conversations are never read.
package antigravity

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/source/sqlitedb"
	"github.com/daemonx666/code-speed/internal/usage"
)

// Antigravity is the source id.
const Antigravity usage.Source = "antigravity"

func init() {
	source.Register(source.Spec{
		Source: Antigravity,
		Label:  "Antigravity",
		Color:  "#22c55e",
		Detect: Detect,
		Poller: func() source.Poller { return &poller{} },
		Every:  5 * time.Second,
	})
}

// Stores lists the store folders (the language server's --app_data_dir).
// A folder ending in -backup is never one of them.
func Stores(home string) []string {
	if home == "" {
		return nil
	}
	g := filepath.Join(home, ".gemini")
	out := []string{
		filepath.Join(g, "antigravity-cli"),
		filepath.Join(g, "antigravity"),
		filepath.Join(g, "antigravity-ide"),
	}
	if runtime.GOOS == "windows" {
		base := filepath.Join(home, ".t3", "userdata", "providers", "antigravity")
		if ents, err := os.ReadDir(base); err == nil {
			var names []string
			for _, e := range ents {
				if e.Type()&os.ModeSymlink == 0 && e.IsDir() {
					names = append(names, e.Name())
				}
			}
			sort.Strings(names)
			for _, n := range names {
				s := filepath.Join(base, n, "antigravity-acp")
				if st, err := os.Stat(filepath.Join(s, "conversations")); err == nil && st.IsDir() {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// Detect reports whether any Antigravity store folder exists. Only the
// folders are checked, never their contents.
func Detect(home string) bool {
	for _, s := range Stores(home) {
		if st, err := os.Stat(s); err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

var uuidDB = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\.db$`)

type dbFile struct {
	path string
	mod  time.Time // the newer of the .db and its -wal
}

// discover lists the conversation databases of the stores: regular files
// (never symlinks) whose name is a UUID followed by .db.
func discover(home string) []dbFile {
	var out []dbFile
	for _, s := range Stores(home) {
		if strings.HasSuffix(s, "-backup") {
			continue
		}
		dir := filepath.Join(s, "conversations")
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.Type().IsRegular() || !uuidDB.MatchString(e.Name()) {
				continue
			}
			p := filepath.Join(dir, e.Name())
			st, err := os.Stat(p)
			if err != nil {
				continue
			}
			f := dbFile{p, st.ModTime()}
			if w, err := os.Stat(p + "-wal"); err == nil && w.ModTime().After(f.mod) {
				f.mod = w.ModTime()
			}
			out = append(out, f)
		}
	}
	return out
}

type poller struct {
	home string // set by tests; else the user's home

	started bool
	seen    map[string]time.Time // path → modification time last read
	sent    map[string]usage.AgentState
	raw     map[string]usage.AgentState // the state as read, before staleness
}

func (p *poller) homeDir() string {
	if p.home != "" {
		return p.home
	}
	h, _ := os.UserHomeDir()
	return h
}

// staleAfter is how long a phase lasts without a new line (spec 6.7).
const staleAfter = 300_000

func (p *poller) Poll(ctx context.Context, since, now time.Time, d *source.Dedup, sink source.Sink) error {
	if !p.started {
		p.started = true
		p.seen = map[string]time.Time{}
		p.sent = map[string]usage.AgentState{}
		p.raw = map[string]usage.AgentState{}
		return p.poll(ctx, since, now, d, sink, true)
	}
	return p.poll(ctx, since, now, d, sink, false)
}

func (p *poller) poll(ctx context.Context, since, now time.Time, d *source.Dedup, sink source.Sink, first bool) error {
	files := discover(p.homeDir())
	// the oldest first: the first database that emits a key owns it
	sort.Slice(files, func(i, j int) bool {
		if !files[i].mod.Equal(files[j].mod) {
			return files[i].mod.Before(files[j].mod)
		}
		return files[i].path < files[j].path
	})
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if m, ok := p.seen[f.path]; ok && m.Equal(f.mod) {
			continue
		}
		if first && f.mod.Before(since.Add(-15*time.Minute)) {
			p.seen[f.path] = f.mod // read completely when it first changes
			continue
		}
		res, err := readDB(ctx, f.path)
		if err != nil || res == nil {
			if err == nil {
				p.seen[f.path] = f.mod // not a conversation database
			}
			continue
		}
		p.seen[f.path] = f.mod
		st := res.state
		var modelIdx, ctxIdx int64 = -1, -1
		for i, r := range res.responses {
			if d != nil {
				if mine, _ := d.Claim(r.Key, f.path); !mine {
					continue // owned by another database
				}
			}
			idx := res.idxs[i]
			if r.Model != "" && idx > modelIdx {
				modelIdx, st.Model = idx, r.Model
			}
			if idx > ctxIdx {
				ctxIdx, st.Context = idx, r.Input
			}
			if r.EndMs >= since.UnixMilli() {
				sink.Response(r)
			}
		}
		p.raw[f.path] = st
	}
	for path, st := range p.raw {
		if st.Phase != usage.Idle && now.UnixMilli()-st.LastLineMs > staleAfter {
			st.Phase, st.InFlightMs = usage.Idle, 0
		}
		if old, ok := p.sent[path]; ok && old == st {
			continue
		}
		p.sent[path] = st
		sink.Agent(st)
	}
	return nil
}

const maxBlob = 1 << 20

type stepRow struct {
	idx, typ, status int64
	meta             []byte
}

type result struct {
	responses []usage.Response
	idxs      []int64 // the step idx of each response
	state     usage.AgentState
}

// readDB reads one conversation database completely. It returns nil, nil
// for a database that is not a supported conversation database.
func readDB(ctx context.Context, path string) (*result, error) {
	db, _, err := sqlitedb.Open(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	if !hasSteps(ctx, db) {
		return nil, nil
	}
	conv := strings.TrimSuffix(filepath.Base(path), ".db")

	// steps, the later row of an idx replacing the earlier
	rows, err := db.QueryContext(ctx, `SELECT idx, step_type, status, CASE WHEN length(metadata) <= 1048576 THEN metadata END FROM steps ORDER BY idx, rowid`)
	if err != nil {
		return nil, err
	}
	steps := map[int64]stepRow{}
	for rows.Next() {
		var idx, typ, status sql.NullInt64
		var meta []byte
		if err := rows.Scan(&idx, &typ, &status, &meta); err != nil {
			rows.Close()
			return nil, err
		}
		steps[idx.Int64] = stepRow{idx.Int64, typ.Int64, status.Int64, meta}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	gens := readGen(ctx, db)
	execs := readExec(ctx, db)
	traj := readTrajectory(ctx, db)

	idxs := make([]int64, 0, len(steps))
	for i := range steps {
		idxs = append(idxs, i)
	}
	sort.Slice(idxs, func(i, j int) bool { return idxs[i] < idxs[j] })

	st := usage.AgentState{
		Agent:    path,
		Source:   Antigravity,
		Project:  traj.project,
		Subagent: traj.subagent,
		Phase:    usage.Idle,
	}
	lastLine := traj.created
	type row struct {
		step   stepRow
		stamps rowTimes
	}
	var latest *row
	var out []usage.Response
	var respIdx []int64

	for _, i := range idxs {
		s := steps[i]
		if s.meta == nil {
			continue
		}
		mt, ok := decodeMeta(s.meta)
		if !ok {
			continue
		}
		if traj.created > 0 && mt.created > 0 && mt.created < traj.created-2000 {
			continue
		}
		for _, t := range mt.all() {
			lastLine = max(lastLine, t)
		}
		latest = &row{s, mt}
		if s.typ != 15 || !terminal(s.status) || !mt.hasUsage {
			continue
		}
		u := mt.usage
		key := u.responseID
		if key == "" {
			key = u.messageID
		}
		if key == "" {
			key = "antigravity:" + conv + ":" + itoa(s.idx)
		}
		end := firstPositive(mt.finished, mt.lastChunk, mt.completed)
		start := mt.created
		if end == 0 {
			end, start = mt.latest(), 0
			if end == 0 {
				continue
			}
		}
		if s.status == 6 || s.status == 7 || s.status == 12 {
			start = 0
		}
		model := modelOf(s.idx, gens, execs)
		outTok := u.output
		if outTok == 0 {
			outTok = source.Sat(u.thinking, u.response)
		}
		in := source.Sat(u.input, u.cacheRead)
		out = append(out, usage.Response{
			Key:     key,
			Source:  Antigravity,
			Agent:   path,
			Project: traj.project,
			Model:   model,
			StartMs: start,
			EndMs:   end,
			Tokens: usage.Tokens{
				Input:       in,
				CachedInput: u.cacheRead,
				Output:      outTok,
				Reasoning:   u.thinking,
			},
			Final: true,
		})
		respIdx = append(respIdx, s.idx)
	}
	st.LastLineMs = lastLine

	// phase
	if latest != nil {
		s, mt := latest.step, latest.stamps
		ended := false
		for _, e := range execs {
			if e.term != 0 && e.last >= s.idx {
				ended = true
			}
		}
		if !ended {
			switch {
			case s.typ == 15 && !terminal(s.status):
				st.Phase, st.InFlightMs = usage.Generating, mt.created
			case s.typ == 15:
			case s.typ == 14 && (s.status == 6 || s.status == 7 || s.status == 12):
			case s.typ == 14:
				st.Phase, st.InFlightMs = usage.Generating, mt.created
			case !terminal(s.status):
				st.Phase = usage.Tools
			case s.status == 3:
				st.Phase, st.InFlightMs = usage.Generating, firstPositive(mt.completed, mt.created)
			}
		}
	}
	return &result{responses: out, idxs: respIdx, state: st}, nil
}

func terminal(status int64) bool {
	switch status {
	case 1, 2, 8, 9, 11:
		return false
	}
	return true
}

func firstPositive(v ...int64) int64 {
	for _, x := range v {
		if x > 0 {
			return x
		}
	}
	return 0
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// hasSteps checks that the steps table has the columns read.
func hasSteps(ctx context.Context, db *sql.DB) bool {
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info('steps')`)
	if err != nil {
		return false
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			have[n] = true
		}
	}
	return have["idx"] && have["step_type"] && have["status"] && have["metadata"]
}

// rowTimes holds the decoded CortexStepMetadata fields that are read.
type rowTimes struct {
	created, viewable, finished, completed, lastChunk, started int64
	hasUsage                                                   bool
	usage                                                      usageStats
}

type usageStats struct {
	input, output, cacheRead, thinking, response int64
	messageID, responseID                        string
}

func (r rowTimes) all() []int64 {
	var out []int64
	for _, t := range []int64{r.created, r.viewable, r.finished, r.completed, r.lastChunk, r.started} {
		if t > 0 {
			out = append(out, t)
		}
	}
	return out
}

func (r rowTimes) latest() int64 {
	var m int64
	for _, t := range r.all() {
		m = max(m, t)
	}
	return m
}

const maxTokens = 10_000_000

func decodeMeta(b []byte) (rowTimes, bool) {
	var r rowTimes
	fs, ok := walk(b)
	if !ok {
		return r, false
	}
	m := msg(fs)
	for _, t := range []struct {
		n   int
		dst *int64
	}{{1, &r.created}, {6, &r.viewable}, {7, &r.finished}, {8, &r.completed}, {22, &r.lastChunk}, {32, &r.started}} {
		v, ok := m.timestamp(t.n)
		if !ok {
			return r, false
		}
		*t.dst = v
	}
	u, present, ok := m.sub(9)
	if !ok {
		return r, false
	}
	if present {
		r.usage = usageStats{
			input: u.num(2), output: u.num(3), cacheRead: u.num(5),
			thinking: u.num(9), response: u.num(10),
			messageID: u.str(7), responseID: u.str(11),
		}
		for _, v := range []int64{r.usage.input, r.usage.output, r.usage.cacheRead, r.usage.thinking, r.usage.response} {
			if v > maxTokens {
				return r, false
			}
		}
		r.usage.input, r.usage.output = max(r.usage.input, 0), max(r.usage.output, 0)
		r.usage.cacheRead, r.usage.thinking = max(r.usage.cacheRead, 0), max(r.usage.thinking, 0)
		r.usage.response = max(r.usage.response, 0)
		r.hasUsage = r.usage.input > 0 || r.usage.output > 0 || r.usage.cacheRead > 0 || r.usage.thinking > 0 || r.usage.response > 0
	}
	return r, true
}

type genRow struct {
	idx     int64
	steps   []int64
	model   string // response_model
	display string
}

func readGen(ctx context.Context, db *sql.DB) []genRow {
	rows, err := db.QueryContext(ctx, `SELECT idx, CASE WHEN typeof(size) = 'integer' AND size <= 1048576 AND length(data) <= 1048576 THEN data END FROM gen_metadata ORDER BY idx, rowid`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	byIdx := map[int64]genRow{}
	for rows.Next() {
		var idx sql.NullInt64
		var data []byte
		if rows.Scan(&idx, &data) != nil || data == nil {
			continue
		}
		fs, ok := walk(data)
		if !ok {
			continue
		}
		m := msg(fs)
		cm, _, ok := m.sub(1)
		if !ok {
			continue
		}
		byIdx[idx.Int64] = genRow{idx.Int64, m.indices(2), cm.str(19), cm.str(21)}
	}
	out := make([]genRow, 0, len(byIdx))
	for _, g := range byIdx {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].idx < out[j].idx })
	return out
}

type execRow struct {
	idx   int64
	term  int64
	last  int64
	model string
}

func readExec(ctx context.Context, db *sql.DB) []execRow {
	rows, err := db.QueryContext(ctx, `SELECT idx, CASE WHEN length(data) <= 1048576 THEN data END FROM executor_metadata ORDER BY idx, rowid`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	byIdx := map[int64]execRow{}
	for rows.Next() {
		var idx sql.NullInt64
		var data []byte
		if rows.Scan(&idx, &data) != nil || data == nil {
			continue
		}
		fs, ok := walk(data)
		if !ok {
			continue
		}
		m := msg(fs)
		e := execRow{idx: idx.Int64, term: m.num(1), last: m.num(3)}
		cc, _, ok := m.sub(10)
		if !ok {
			continue
		}
		pc, _, ok := cc.sub(1)
		if !ok {
			continue
		}
		e.model = pc.str(28)
		byIdx[idx.Int64] = e
	}
	out := make([]execRow, 0, len(byIdx))
	for _, e := range byIdx {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].idx < out[j].idx })
	return out
}

// modelOf resolves a planner row's model from the metadata rows read.
func modelOf(idx int64, gens []genRow, execs []execRow) string {
	for _, g := range gens {
		for _, s := range g.steps {
			if s == idx {
				if g.model != "" {
					return g.model
				}
				if g.display != "" {
					return g.display
				}
				goto executor
			}
		}
	}
executor:
	var best *execRow
	for i := range execs {
		e := &execs[i]
		if e.last >= idx && (best == nil || e.last < best.last) {
			best = e
		}
	}
	if best != nil {
		return best.model
	}
	return ""
}

type trajectory struct {
	created  int64
	project  string
	subagent bool
}

func readTrajectory(ctx context.Context, db *sql.DB) trajectory {
	var t trajectory
	var data []byte
	err := db.QueryRowContext(ctx, `SELECT CASE WHEN length(data) <= 1048576 THEN data END FROM trajectory_metadata_blob ORDER BY rowid LIMIT 1`).Scan(&data)
	if err != nil || data == nil {
		return t
	}
	fs, ok := walk(data)
	if !ok {
		return t
	}
	m := msg(fs)
	t.created, ok = m.timestamp(2)
	if !ok {
		t.created = 0
	}
	if u := m.firstStr(7); u != "" {
		t.project = projectOf(u)
	}
	if t.project == "" {
		if w, _, ok := m.firstSub(1); ok {
			t.project = projectOf(w.str(1))
		}
	}
	_, spec, _ := m.sub(8)
	t.subagent = m.str(5) != "" || m.num(17) > 0 || spec
	return t
}
