// Package cline reads Cline's local task logs, whole JSON files that are
// rewritten as a task goes on, so the source is polled rather than tailed.
//
// Two layouts exist:
//
//	A (legacy, Cline 3.x and old tasks): <globalStorage>/tasks/<taskId>/
//	  ui_messages.json (one "api_req_started" say per request, its text a
//	  JSON string with tokensIn/tokensOut/cacheWrites/cacheReads/cost) and
//	  api_conversation_history.json (assistant entries with metrics and ts,
//	  which give the end of each request).
//	B (SDK era, extension 4.x and the CLI): <dataDir>/sessions/<id>/
//	  <stem>.messages.json, assistant messages carrying metrics.
//
// Roo Code and Kilo Code are Cline forks; they are read as layout A under
// their own extension ids.
package cline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/usage"
)

// Source ids.
const (
	Cline usage.Source = "cline"
	Roo   usage.Source = "roo-code"
	Kilo  usage.Source = "kilo-code"
)

const (
	clineExt = "saoudrizwan.claude-dev"
	rooExt   = "rooveterinaryinc.roo-cline"
	kiloExt  = "kilocode.kilo-code"
)

// editors whose user-data folder holds extension globalStorage.
var editors = []string{"Code", "Code - Insiders", "Cursor", "Windsurf", "VSCodium"}

func init() {
	for _, k := range []kind{
		{src: Cline, label: "Cline", color: "#06b6d4", ext: clineExt, sdk: true},
		// UNVERIFIED: Roo Code's and Kilo Code's on-disk layout is assumed to
		// match legacy Cline (they forked it). Their api_conversation_history
		// carries no metrics, so speed is only measured when the assistant
		// entries match the requests one to one.
		{src: Roo, label: "Roo Code", color: "#f472b6", ext: rooExt},
		{src: Kilo, label: "Kilo Code", color: "#facc15", ext: kiloExt},
	} {
		k := k
		source.Register(source.Spec{
			Source: k.src,
			Label:  k.label,
			Color:  k.color,
			Detect: func(home string) bool { return k.detect(env{home: home, goos: runtime.GOOS, getenv: os.Getenv}) },
			Poller: func() source.Poller { return &poller{k: k} },
			Every:  5 * time.Second,
		})
	}
}

type kind struct {
	src   usage.Source
	label string
	color string
	ext   string // VS Code extension id
	sdk   bool   // also reads layout B and ~/.cline/data (Cline only)
}

// env is where paths come from; tests replace it.
type env struct {
	home   string
	goos   string
	getenv func(string) string
}

func (e env) userData(app string) string {
	switch e.goos {
	case "darwin":
		return filepath.Join(e.home, "Library", "Application Support", app)
	case "windows":
		if a := e.getenv("APPDATA"); a != "" {
			return filepath.Join(a, app)
		}
		return filepath.Join(e.home, "AppData", "Roaming", app)
	}
	return filepath.Join(e.home, ".config", app)
}

// dataDir is Cline's SDK data folder.
func (e env) dataDir() string {
	if d := strings.TrimSpace(e.getenv("CLINE_DATA_DIR")); d != "" {
		return d
	}
	if d := strings.TrimSpace(e.getenv("CLINE_DIR")); d != "" {
		return filepath.Join(d, "data")
	}
	return filepath.Join(e.home, ".cline", "data")
}

func (e env) sessionsDir() string {
	if d := strings.TrimSpace(e.getenv("CLINE_SESSION_DATA_DIR")); d != "" {
		return d
	}
	return filepath.Join(e.dataDir(), "sessions")
}

// storeA is one folder holding tasks/<id>/ (layout A).
type storeA struct {
	root string // the folder with tasks/ and state/
}

func (k kind) storesA(e env) []storeA {
	var out []storeA
	for _, app := range editors {
		out = append(out, storeA{filepath.Join(e.userData(app), "User", "globalStorage", k.ext)})
	}
	if k.sdk {
		out = append(out, storeA{e.dataDir()})
	}
	return out
}

func (k kind) detect(e env) bool {
	for _, s := range k.storesA(e) {
		if isDir(filepath.Join(s.root, "tasks")) {
			return true
		}
	}
	return k.sdk && isDir(e.sessionsDir())
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

type fileSig struct {
	mod  time.Time
	size int64
}

func sigOf(p string) (fileSig, bool) {
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() {
		return fileSig{}, false
	}
	return fileSig{st.ModTime(), st.Size()}, true
}

type poller struct {
	k  kind
	e  *env // set by tests
	sz map[string][]fileSig
}

func (p *poller) env() env {
	if p.e != nil {
		return *p.e
	}
	h, _ := os.UserHomeDir()
	return env{home: h, goos: runtime.GOOS, getenv: os.Getenv}
}

// job is one unit of rereading: a file set read together.
type job struct {
	agent string   // the key owner, the main file's path
	files []string // files whose signatures decide a reread
	read  func() []usage.Response
}

func (p *poller) Poll(ctx context.Context, since, now time.Time, d *source.Dedup, sink source.Sink) error {
	if p.sz == nil {
		p.sz = map[string][]fileSig{}
	}
	e := p.env()
	var jobs []job
	for _, s := range p.k.storesA(e) {
		jobs = append(jobs, p.tasksA(s)...)
	}
	if p.k.sdk {
		jobs = append(jobs, p.sessionsB(e)...)
	}
	for _, j := range jobs {
		if err := ctx.Err(); err != nil {
			return err
		}
		sigs := make([]fileSig, len(j.files))
		var newest time.Time
		for i, f := range j.files {
			if s, ok := sigOf(f); ok {
				sigs[i] = s
				if s.mod.After(newest) {
					newest = s.mod
				}
			}
		}
		if newest.IsZero() || newest.Before(since) {
			continue
		}
		if old, ok := p.sz[j.agent]; ok && sameSigs(old, sigs) {
			continue
		}
		p.sz[j.agent] = sigs
		for _, r := range j.read() {
			if r.EndMs < since.UnixMilli() {
				continue
			}
			if d != nil {
				if mine, _ := d.Claim(r.Key, j.agent); !mine {
					continue // the same task seen under another store
				}
			}
			sink.Response(r)
		}
	}
	return nil
}

func sameSigs(a, b []fileSig) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].mod.Equal(b[i].mod) || a[i].size != b[i].size {
			return false
		}
	}
	return true
}

// loadCwds reads <root>/state/taskHistory.json: task id → working folder.
func loadCwds(root string) map[string]string {
	b, err := os.ReadFile(filepath.Join(root, "state", "taskHistory.json"))
	if err != nil {
		return nil
	}
	var items []struct {
		ID  string `json:"id"`
		Cwd string `json:"cwdOnTaskInitialization"`
	}
	if json.Unmarshal(b, &items) != nil {
		return nil
	}
	m := make(map[string]string, len(items))
	for _, it := range items {
		if it.ID != "" && it.Cwd != "" {
			m[it.ID] = it.Cwd
		}
	}
	return m
}

func subdirs(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func (p *poller) tasksA(s storeA) []job {
	var jobs []job
	var cwds map[string]string
	loaded := false
	for _, id := range subdirs(filepath.Join(s.root, "tasks")) {
		dir := filepath.Join(s.root, "tasks", id)
		ui := filepath.Join(dir, "ui_messages.json")
		api := filepath.Join(dir, "api_conversation_history.json")
		meta := filepath.Join(dir, "task_metadata.json")
		id := id
		jobs = append(jobs, job{
			agent: ui,
			files: []string{ui, api, meta},
			read: func() []usage.Response {
				if !loaded {
					cwds, loaded = loadCwds(s.root), true
				}
				return readTaskA(p.k, id, ui, api, meta, source.Project(cwds[id]))
			},
		})
	}
	return jobs
}

func (p *poller) sessionsB(e env) []job {
	var jobs []job
	root := e.sessionsDir()
	for _, id := range subdirs(root) {
		dir := filepath.Join(root, id)
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range ents {
			name := f.Name()
			if !f.Type().IsRegular() || !strings.HasSuffix(name, ".messages.json") {
				continue
			}
			path := filepath.Join(dir, name)
			stem := strings.TrimSuffix(name, ".messages.json")
			jobs = append(jobs, job{
				agent: path,
				files: []string{path},
				read:  func() []usage.Response { return readSessionB(p.k, stem, path) },
			})
		}
	}
	return jobs
}
