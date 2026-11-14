package source

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/daemonx666/code-speed/internal/source/codextrace"
	"github.com/daemonx666/code-speed/internal/usage"
)

type sink struct {
	resp   map[string]usage.Response
	order  []string
	quotas []usage.Quota
}

func newSink() *sink { return &sink{resp: map[string]usage.Response{}} }

func (s *sink) Response(r usage.Response) {
	if _, ok := s.resp[r.Key]; !ok {
		s.order = append(s.order, r.Key)
	}
	s.resp[r.Key] = r
}
func (s *sink) Quota(q usage.Quota)    { s.quotas = append(s.quotas, q) }
func (s *sink) Agent(usage.AgentState) {}

func feed(p Parser, lines string) {
	for _, l := range strings.Split(strings.TrimSpace(lines), "\n") {
		p.Line([]byte(strings.TrimSpace(l)))
	}
}

func ms(s string) int64 {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t.UnixMilli()
}

const claudeLog = `
{"type":"user","timestamp":"2026-11-14T10:00:00.000Z","cwd":"/work/demo","message":{"role":"user","content":"hi"}}
{"type":"assistant","timestamp":"2026-11-14T10:00:02.000Z","message":{"id":"msg_1","model":"claude-opus-5-5","stop_reason":null,"usage":{"input_tokens":10,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000,"output_tokens":5},"content":[{"type":"text","text":"he"}]}}
{"type":"assistant","timestamp":"2026-11-14T10:00:04.000Z","message":{"id":"msg_1","model":"claude-opus-5-5","stop_reason":"end_turn","usage":{"input_tokens":10,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000,"output_tokens":200,"cache_creation":{"ephemeral_1h_input_tokens":40}},"content":[{"type":"text","text":"hello"}]}}
`

func TestClaudeMergesOneMessage(t *testing.T) {
	s := newSink()
	p := newClaude("/a.jsonl", NewDedup(), s)
	feed(p, claudeLog)
	if len(s.order) != 1 {
		t.Fatalf("responses = %d, want 1", len(s.order))
	}
	r := s.resp["msg_1"]
	want := usage.Tokens{Input: 1110, CachedInput: 1000, CacheWrite: 100, CacheWrite1h: 40, Output: 200}
	if r.Tokens != want {
		t.Fatalf("tokens = %+v, want %+v", r.Tokens, want)
	}
	if r.StartMs != ms("2026-11-14T10:00:00Z") || r.EndMs != ms("2026-11-14T10:00:04Z") || r.FirstMs != ms("2026-11-14T10:00:02Z") {
		t.Fatalf("timing = %d %d %d", r.StartMs, r.FirstMs, r.EndMs)
	}
	if !r.Final || !r.Measured() {
		t.Fatal("a stopped message is final and measured")
	}
	if sp, _ := r.Speed(); sp != 50 { // 200 tokens in 4 s
		t.Fatalf("speed = %v, want 50", sp)
	}
	if st := p.State(); st.Phase != usage.Idle || st.Project != "demo" || st.Model != "claude-opus-5-5" {
		t.Fatalf("state = %+v", st)
	}
}

func TestClaudeCopiedHistoryCountsOnce(t *testing.T) {
	d := NewDedup()
	s := newSink()
	feed(newClaude("/a.jsonl", d, s), claudeLog)
	feed(newClaude("/fork.jsonl", d, s), claudeLog)
	if len(s.order) != 1 || s.resp["msg_1"].Agent != "/a.jsonl" {
		t.Fatalf("a resumed session's copy counted again: %v", s.order)
	}
}

func TestClaudePhases(t *testing.T) {
	s := newSink()
	p := newClaude("/a.jsonl", NewDedup(), s)
	feed(p, `{"type":"api-request","timestamp":"2026-11-14T10:00:00Z"}`)
	if st := p.State(); st.Phase != usage.Generating || st.InFlightMs != ms("2026-11-14T10:00:00Z") {
		t.Fatalf("api-request: %+v", st)
	}
	feed(p, `{"type":"assistant","timestamp":"2026-11-14T10:00:01Z","message":{"id":"m","model":"x","stop_reason":"tool_use","usage":{"output_tokens":3}}}`)
	if p.State().Phase != usage.Tools {
		t.Fatalf("tool_use: %v", p.State().Phase)
	}
	feed(p, `{"type":"user","timestamp":"2026-11-14T10:00:05Z","message":{"content":[{"type":"text","text":"[Request interrupted by user]"}]}}`)
	if p.State().Phase != usage.Idle {
		t.Fatalf("interrupted: %v", p.State().Phase)
	}
	feed(p, `{"type":"assistant","timestamp":"2026-11-14T10:00:06Z","message":{"id":"<x>","model":"<synthetic>"}}`)
	if _, ok := s.resp["<x>"]; ok {
		t.Fatal("a synthetic message is no response")
	}
}

func TestTokenValues(t *testing.T) {
	s := newSink()
	feed(newClaude("/a.jsonl", NewDedup(), s), `{"type":"assistant","timestamp":"2026-11-14T10:00:00Z","message":{"id":"m","model":"x","stop_reason":"end_turn","usage":{"input_tokens":12.9,"output_tokens":-7,"cache_read_input_tokens":"5","cache_creation_input_tokens":1e20}}}`)
	got := s.resp["m"].Tokens
	if got.CachedInput != 0 || got.Output != 0 || got.CacheWrite != TokCap || got.Input != TokCap {
		t.Fatalf("tokens = %+v", got)
	}
}

const codexLog = `
{"timestamp":"2026-11-14T09:00:00.000Z","type":"session_meta","payload":{"id":"th1","cwd":"/work/api","model_provider":"openai"}}
{"timestamp":"2026-11-14T09:00:00.100Z","type":"turn_context","payload":{"model":"gpt-6.1-terra","cwd":"/work/api"}}
{"timestamp":"2026-11-14T09:00:01.000Z","type":"event_msg","payload":{"type":"user_message"}}
{"timestamp":"2026-11-14T09:00:02.000Z","type":"response_item","payload":{"type":"reasoning"}}
{"timestamp":"2026-11-14T09:00:05.000Z","type":"token_usage_record","payload":{"response_id":"resp_1","usage":{"input_tokens":5000,"cached_input_tokens":4000,"output_tokens":400,"reasoning_output_tokens":100}}}
{"timestamp":"2026-11-14T09:00:05.100Z","type":"event_msg","payload":{"type":"token_count","info":{},"rate_limits":{"limit_id":"codex","primary":{"used_percent":23.0,"window_minutes":300,"resets_at":1794897479},"secondary":{"used_percent":40,"window_minutes":10080,"resets_at":1795000000},"plan_type":"pro"}}}
{"timestamp":"2026-11-14T09:00:06.000Z","type":"event_msg","payload":{"type":"task_complete"}}
`

func TestCodexModernUsageAndQuota(t *testing.T) {
	s := newSink()
	p := newCodex("/r.jsonl", NewDedup(), s)
	feed(p, codexLog)
	r, ok := s.resp["resp_1"]
	if !ok || len(s.order) != 1 {
		t.Fatalf("responses = %v", s.order)
	}
	if r.Model != "gpt-6.1-terra" || r.Provider != "openai" || r.Project != "api" {
		t.Fatalf("meta = %+v", r)
	}
	if r.StartMs != ms("2026-11-14T09:00:01Z") || r.FirstMs != ms("2026-11-14T09:00:02Z") || r.EndMs != ms("2026-11-14T09:00:05Z") {
		t.Fatalf("timing = %d %d %d", r.StartMs, r.FirstMs, r.EndMs)
	}
	if sp, _ := r.Speed(); sp != 100 {
		t.Fatalf("speed = %v, want 100", sp)
	}
	if len(s.quotas) != 1 || len(s.quotas[0].Windows) != 2 {
		t.Fatalf("quotas = %+v", s.quotas)
	}
	w := s.quotas[0].Windows
	if w[0].Kind != "fiveHour" || w[0].UsedPercent != 23 || w[0].ResetsAtMs != 1794897479000 || w[1].Kind != "sevenDay" {
		t.Fatalf("windows = %+v", w)
	}
	if p.State().Phase != usage.Idle {
		t.Fatalf("phase = %v", p.State().Phase)
	}
}

func TestCodexLegacyDeltas(t *testing.T) {
	s := newSink()
	feed(newCodex("/r.jsonl", NewDedup(), s), `
{"timestamp":"2026-11-14T09:00:00Z","type":"session_meta","payload":{"id":"th2"}}
{"timestamp":"2026-11-14T09:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110},"last_token_usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110}}}}
{"timestamp":"2026-11-14T09:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110}}}}
{"timestamp":"2026-11-14T09:00:03Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":250,"output_tokens":30,"total_tokens":280}}}}
`)
	if len(s.order) != 2 {
		t.Fatalf("responses = %v (a repeated count is no response)", s.order)
	}
	if got := s.resp[s.order[1]].Tokens; got.Input != 150 || got.Output != 20 {
		t.Fatalf("delta = %+v", got)
	}
}

func TestCodexCopiedHistoryIgnored(t *testing.T) {
	s := newSink()
	feed(newCodex("/r.jsonl", NewDedup(), s), `
{"timestamp":"2026-11-14T09:00:00Z","type":"session_meta","payload":{"id":"child"}}
{"timestamp":"2026-11-14T08:00:00Z","type":"token_usage_record","payload":{"response_id":"parent_resp","usage":{"output_tokens":9}}}
`)
	if len(s.order) != 0 {
		t.Fatal("a parent's copied record counted in the fork")
	}
}

func TestTailReadsAppendsAndWaitsForWholeLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	lines := strings.Split(strings.TrimSpace(claudeLog), "\n")
	if err := os.WriteFile(path, []byte(lines[0]+"\n"+lines[1][:40]), 0o600); err != nil {
		t.Fatal(err)
	}
	d, s := NewDedup(), newSink()
	tl := NewTail(Found{Source: usage.ClaudeCode, Path: path}, d, s)
	if err := tl.Read(d, s); err != nil {
		t.Fatal(err)
	}
	if len(s.order) != 0 {
		t.Fatal("a half-written line was parsed")
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(lines[1][40:] + "\n" + lines[2] + "\n")
	f.Close()
	if err := tl.Read(d, s); err != nil {
		t.Fatal(err)
	}
	if r := s.resp["msg_1"]; r.Output != 200 || !r.Final {
		t.Fatalf("after append: %+v", r)
	}
}

func TestDiscoverSkipsOldDayFolders(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "2020", "01", "01")
	t0 := time.Now()
	now := filepath.Join(root, t0.Format("2006"), t0.Format("01"), t0.Format("02"))
	for _, d := range []string{old, now} {
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "rollout-x.jsonl"), []byte("{}\n"), 0o600)
	}
	got := Discover([]Root{{usage.Codex, root}}, time.Now().Add(-time.Hour))
	if len(got) != 1 || !strings.HasPrefix(got[0].Path, now) {
		t.Fatalf("found %+v", got)
	}
}

func TestClaudeContextIsTheLatestRequestsInput(t *testing.T) {
	s := newSink()
	p := newClaude("/a.jsonl", NewDedup(), s)
	feed(p, claudeLog)
	if st := p.State(); st.Context != 1110 || st.ContextWindow != 1_000_000 {
		t.Fatalf("context = %d / %d, want 1110 / 1M", st.Context, st.ContextWindow)
	}
}

func TestClaudeWindow(t *testing.T) {
	for _, c := range []struct {
		model string
		ctx   int64
		want  int64
	}{
		{"claude-opus-5-5", 10, 1_000_000},
		{"claude-sonnet-5-5", 10, 1_000_000},
		{"claude-haiku-5-5", 10, 1_000_000},
		{"claude-fable-5-2", 10, 1_000_000},
		{"claude-haiku-4-5", 10, 200_000},
		{"claude-sonnet-4-5", 10, 200_000},
		{"claude-sonnet-4-5", 300_000, 1_000_000}, // its 1M beta
		{"kimi-k3", 10, 0},
	} {
		if got := ClaudeWindow(c.model, c.ctx); got != c.want {
			t.Errorf("ClaudeWindow(%q, %d) = %d, want %d", c.model, c.ctx, got, c.want)
		}
	}
}

func TestCodexContextAndWindowFromTokenCount(t *testing.T) {
	s := newSink()
	p := newCodex("/r.jsonl", NewDedup(), s)
	feed(p, codexLog+`
{"timestamp":"2026-11-14T09:00:07Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":91000,"output_tokens":10,"total_tokens":91010},"model_context_window":475000}}}`)
	if st := p.State(); st.Context != 91000 || st.ContextWindow != 475000 {
		t.Fatalf("context = %d / %d, want 91000 / 475000", st.Context, st.ContextWindow)
	}
}

func codexTurnLog(thread, turn, tier, respID string) string {
	return fmt.Sprintf(`
{"timestamp":"2026-11-14T09:00:00.000Z","type":"session_meta","payload":{"id":%q,"cwd":"/w","model_provider":"openai"}}
{"timestamp":"2026-11-14T09:00:00.100Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_id":%q,"thread_settings":{"service_tier":%q}}}
{"timestamp":"2026-11-14T09:00:00.500Z","type":"event_msg","payload":{"type":"task_started","turn_id":%q}}
{"timestamp":"2026-11-14T09:00:00.600Z","type":"turn_context","payload":{"turn_id":%q,"model":"gpt-6.1-sol"}}
{"timestamp":"2026-11-14T09:00:05.000Z","type":"token_usage_record","payload":{"thread_id":%q,"turn_id":%q,"response_id":%q,"usage":{"input_tokens":50,"output_tokens":10}}}
`, thread, thread, tier, turn, turn, thread, turn, respID)
}

func TestCodexFastFromThreadSettings(t *testing.T) {
	for tier, want := range map[string]bool{"priority": true, "default": false} {
		s := newSink()
		feed(newCodex("/r.jsonl", NewDedup(), s), codexTurnLog("th", "turn-1", tier, "r1"))
		if got := s.resp["r1"].Fast; got != want {
			t.Errorf("service_tier %q: Fast = %v, want %v", tier, got, want)
		}
	}
}

func traceDB(t *testing.T, dir string, rows ...[3]string) string {
	t.Helper()
	path := filepath.Join(dir, "logs_9.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`create table if not exists logs (id integer primary key autoincrement, ts integer not null, thread_id text, feedback_log_body text)`); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if _, err := db.Exec(`insert into logs (ts, thread_id, feedback_log_body) values (?, ?, ?)`, time.Now().Unix(), r[0], r[1]+r[2]); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestCodexFastFromTraceDatabase(t *testing.T) {
	dir := t.TempDir()
	traceDB(t, dir, [3]string{"th", `session_loop{thread_id=th}:turn{turn.id=turn-1 model=gpt-6.1-sol}: websocket request: `,
		`{"type":"response.create","model":"gpt-6.1-sol","service_tier":"priority"}`})
	tr := codextrace.New(dir, time.Now().Add(-time.Hour))
	s := newSink()
	p := newCodex("/r.jsonl", NewDedup(), s)
	p.trace = tr
	feed(p, codexTurnLog("th", "turn-1", "default", "r1"))
	feed(p, strings.Replace(codexTurnLog("th", "turn-2", "default", "r2"), "09:00:0", "10:00:0", -1)[1:])
	if !s.resp["r1"].Fast {
		t.Error("a turn the trace shows at priority is not fast")
	}
	if s.resp["r2"].Fast {
		t.Error("a turn without trace evidence is fast")
	}
}

func TestCodexFastTraceArrivesLate(t *testing.T) {
	dir := t.TempDir()
	traceDB(t, dir) // no rows yet
	tr := codextrace.New(dir, time.Now().Add(-time.Hour))
	s := newSink()
	p := newCodex("/r.jsonl", NewDedup(), s)
	p.trace = tr
	feed(p, codexTurnLog("th", "turn-1", "default", "r1"))
	if s.resp["r1"].Fast {
		t.Fatal("fast before any evidence")
	}
	traceDB(t, dir, [3]string{"th", `{turn_id=turn-1 model=gpt-6.1-sol}: Submission sub=Submission { id: "turn-1", op: UserTurn { service_tier: Some(Some("priority")) } }`, ""})
	time.Sleep(2100 * time.Millisecond) // the trace re-reads at most every 2 s
	feed(p, `{"timestamp":"2026-11-14T09:00:06.000Z","type":"event_msg","payload":{"type":"task_complete"}}`)
	if !s.resp["r1"].Fast {
		t.Fatal("the response was not re-emitted as fast")
	}
}

func zstFile(t *testing.T, path, text string) {
	t.Helper()
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, enc.EncodeAll([]byte(text), nil), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCodexZstRollout(t *testing.T) {
	sp, _ := Lookup(usage.Codex)
	if !sp.Match("/a/rollout-1.jsonl") || !sp.Match("/a/rollout-1.jsonl.zst") || sp.Match("/a/rollout-1.json") {
		t.Fatal("Match must accept .jsonl and .jsonl.zst")
	}
	path := filepath.Join(t.TempDir(), "rollout-x.jsonl.zst")
	zstFile(t, path, strings.TrimSpace(codexLog)) // no trailing newline
	d, s := NewDedup(), newSink()
	tl := NewTail(Found{Source: usage.Codex, Path: path}, d, s)
	if err := tl.Read(d, s); err != nil {
		t.Fatal(err)
	}
	if r := s.resp["resp_1"]; r.Output != 400 || r.Model != "gpt-6.1-terra" {
		t.Fatalf("resp_1 = %+v", r)
	}
	// unchanged: not read again
	tl.Parser = nil
	if err := tl.Read(d, s); err != nil {
		t.Fatal(err)
	}
	// changed: read again with a fresh parser
	zstFile(t, path, strings.TrimSpace(codexLog)+"\n"+`{"timestamp":"2026-11-14T09:01:00.000Z","type":"token_usage_record","payload":{"response_id":"resp_2","usage":{"output_tokens":7}}}`)
	if err := tl.Read(d, s); err != nil {
		t.Fatal(err)
	}
	if r := s.resp["resp_2"]; r.Output != 7 {
		t.Fatalf("resp_2 = %+v", r)
	}
}

func TestReasoningEffort(t *testing.T) {
	s := newSink()
	p := newClaude("/a.jsonl", NewDedup(), s)
	// params before type: the order of keys doesn't matter
	feed(p, `{"params":{"model":"claude-opus-5-5","output_config":{"effort":"high"}},"type":"api-request","timestamp":"2026-11-14T10:00:00Z"}`)
	feed(p, `{"type":"assistant","timestamp":"2026-11-14T10:00:02Z","message":{"id":"m1","model":"claude-opus-5-5","stop_reason":"end_turn","usage":{"output_tokens":3}}}`)
	feed(p, `{"type":"api-request","timestamp":"2026-11-14T10:01:00Z","params":{"model":"claude-opus-5-5"}}`)
	feed(p, `{"type":"assistant","timestamp":"2026-11-14T10:01:02Z","message":{"id":"m2","model":"claude-opus-5-5","stop_reason":"end_turn","usage":{"output_tokens":3}}}`)
	if s.resp["m1"].Effort != "high" || s.resp["m2"].Effort != "" {
		t.Fatalf("claude effort %q %q", s.resp["m1"].Effort, s.resp["m2"].Effort)
	}

	s = newSink()
	c := newCodex("/r.jsonl", NewDedup(), s)
	feed(c, strings.Replace(codexLog, `"model":"gpt-6.1-terra",`, `"model":"gpt-6.1-terra","effort":"low",`, 1))
	var got []string
	for _, r := range s.resp {
		got = append(got, r.Effort)
	}
	if len(got) != 1 || got[0] != "low" {
		t.Fatalf("codex effort %v", got)
	}
}
