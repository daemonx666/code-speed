package codextrace

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestParseRow(t *testing.T) {
	cases := []struct {
		name, body, turn, thread, model string
	}{
		{"websocket", `session_loop{thread_id=th1}:turn{turn.id=t1}: websocket request: {"type":"response.create","model":"gpt-6.1-sol","service_tier":"priority","input":[]}`, "t1", "th1", "gpt-6.1-sol"},
		{"websocket turn_id in json", `x: websocket request: {"type":"response.create","service_tier":"priority","turn_id":"t2"}`, "t2", "", ""},
		{"submission", `{thread_id=th3}: Submission sub=Submission { id: "t3", op: UserTurn { service_tier: Some(Some("priority")) } }`, "t3", "th3", ""},
		{"tags", `run_sampling_request{turn_id=t4 model=gpt-6.1-sol}:{"service_tier":"priority"} session_loop{thread_id=th4}`, "t4", "th4", "gpt-6.1-sol"},
		{"default tier", `run_sampling_request{turn_id=t5 model=m}:{"service_tier":"default"}`, "", "", ""},
		{"websocket default", `turn.id=t6 websocket request: {"type":"response.create","service_tier":"default"}`, "", "", ""},
		{"not a create", `turn.id=t7 websocket request: {"type":"response.cancel","service_tier":"priority"}`, "", "", ""},
		{"no turn", `websocket request: {"type":"response.create","service_tier":"priority"}`, "", "", ""},
	}
	for _, c := range cases {
		turn, m, ok := ParseRow(c.body)
		if ok != (c.turn != "") || turn != c.turn || m.Thread != c.thread || m.Model != c.model {
			t.Errorf("%s: got (%q, %+v, %v)", c.name, turn, m, ok)
		}
	}
}

func TestIncrementalScan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs_2.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`create table logs (id integer primary key autoincrement, ts integer not null, thread_id text, feedback_log_body text)`); err != nil {
		t.Fatal(err)
	}
	add := func(ts int64, thread, body string) {
		if _, err := db.Exec(`insert into logs (ts, thread_id, feedback_log_body) values (?, ?, ?)`, ts, thread, body); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().Unix()
	add(now, "th", `turn_id=a model=m {"service_tier":"priority"}`)
	add(now, "th", `turn_id=b model=m {"service_tier":"default"}`)
	add(now-30*86400, "th", `turn_id=old {"service_tier":"priority"}`)
	tr := New(dir, time.Now().Add(-24*time.Hour))
	tr.interval = 0
	tr.Refresh()
	if !tr.Priority("th", "a") || tr.Priority("th", "b") || tr.Priority("th", "old") || tr.Priority("other", "a") || tr.Priority("th", "") {
		t.Fatalf("turns = %v", tr.turns)
	}
	add(now, "th", `turn_id=c model=m {"service_tier":"priority"}`)
	tr.Refresh()
	if !tr.Priority("th", "c") || tr.Turns() != 2 {
		t.Fatalf("turns = %v", tr.turns)
	}
	// throttled
	tr.interval = time.Hour
	add(now, "th", `turn_id=d {"service_tier":"priority"}`)
	if tr.Refresh() || tr.Priority("th", "d") {
		t.Fatal("refresh was not throttled")
	}
}
