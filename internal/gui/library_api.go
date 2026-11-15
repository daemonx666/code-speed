package gui

import (
	"net/http"
	"path/filepath"
	"slices"

	"github.com/daemonx666/code-speed/internal/library"
	"github.com/daemonx666/code-speed/internal/library/mcpfmt"
	"github.com/daemonx666/code-speed/internal/service"
)

// OpenLibrary opens (or brings forward) the library window; set by the app.
var OpenLibrary func()

// PickFolder asks the user for a folder with the system's own dialog and
// answers its path, "" when cancelled; set by the app.
var PickFolder func(title string) string

// libraryRoutes serves the library window's API:
//
//	GET  /api/library            targets, items with their state in each, found items
//	POST /api/library/import     {items:[{kind,name,hash,as}]} (none: every one without a conflict)
//	POST /api/library/server     {old, server, agents}  add or edit a server   → results
//	POST /api/library/skill      {name, path, agents}   add or replace a skill → results
//	POST /api/library/agents     {kind, name, agents}   tick targets           → results
//	POST /api/library/adopt      {kind, name, target}   take an agent's version → results
//	POST /api/library/apply      {overwrite, only, items} → results
//
// A change to one item is written to the agents at once (an apply limited
// to it), as magpie's library does, and answers what was written where.
//	POST /api/library/delete     {kind, name}           → results
//	POST /api/library/project    {add | remove}         → results
//	GET  /api/library/backups;   POST /api/library/restore {id}
//	POST /api/library/pick       {title} → {path}       the system's folder dialog
//	POST /api/library/reveal     {path}                 a path the library knows, in Finder/Explorer
//	POST /api/library/open                              the library window
func libraryRoutes(mux *http.ServeMux, m *library.Manager, post func(http.ResponseWriter, *http.Request, any) bool) {
	fail := func(w http.ResponseWriter, err error) {
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
	results := func(w http.ResponseWriter, res []library.Result, err error) {
		if res == nil {
			res = []library.Result{}
		}
		v := map[string]any{"results": res}
		if err != nil {
			v["error"] = err.Error()
		}
		writeJSON(w, v)
	}
	// applyItems writes the items just changed and answers the results
	applyItems := func(w http.ResponseWriter, items ...string) {
		res, err := m.Apply(library.ApplyOptions{Items: items})
		results(w, res, err)
	}
	mux.HandleFunc("/api/library", func(w http.ResponseWriter, r *http.Request) {
		st, err := m.State()
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, st)
	})
	mux.HandleFunc("/api/library/import", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Items []library.ImportReq }
		if !post(w, r, &body) {
			return
		}
		if err := m.Import(body.Items); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/library/server", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Old    string
			Server mcpfmt.Server
			Agents []string
		}
		if !post(w, r, &body) {
			return
		}
		if err := m.PutServer(body.Old, body.Server, body.Agents); err != nil {
			fail(w, err)
			return
		}
		items := []string{library.KindMCP + "/" + body.Server.Name}
		if body.Old != "" && body.Old != body.Server.Name {
			items = append(items, library.KindMCP+"/"+body.Old) // out of the agents under its old name
		}
		applyItems(w, items...)
	})
	mux.HandleFunc("/api/library/skill", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name, Path string
			Agents     []string
		}
		if !post(w, r, &body) {
			return
		}
		if body.Name == "" {
			body.Name = filepath.Base(body.Path)
		}
		if err := m.PutSkill(body.Name, body.Path, body.Agents); err != nil {
			fail(w, err)
			return
		}
		applyItems(w, library.KindSkill+"/"+body.Name)
	})
	mux.HandleFunc("/api/library/agents", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Kind, Name string
			Agents     []string
		}
		if !post(w, r, &body) {
			return
		}
		if err := m.SetAgents(body.Kind, body.Name, body.Agents); err != nil {
			fail(w, err)
			return
		}
		applyItems(w, body.Kind+"/"+body.Name)
	})
	mux.HandleFunc("/api/library/adopt", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Kind, Name, Target string }
		if !post(w, r, &body) {
			return
		}
		if err := m.Adopt(body.Kind, body.Name, body.Target); err != nil {
			fail(w, err)
			return
		}
		// the library's version changed: the other agents given it get it
		applyItems(w, body.Kind+"/"+body.Name)
	})
	mux.HandleFunc("/api/library/apply", func(w http.ResponseWriter, r *http.Request) {
		var body library.ApplyOptions
		if !post(w, r, &body) {
			return
		}
		res, err := m.Apply(body)
		results(w, res, err)
	})
	mux.HandleFunc("/api/library/delete", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Kind, Name string }
		if !post(w, r, &body) {
			return
		}
		res, err := m.Delete(body.Kind, body.Name)
		results(w, res, err)
	})
	mux.HandleFunc("/api/library/project", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Add, Remove string }
		if !post(w, r, &body) {
			return
		}
		if body.Add != "" {
			if err := m.AddProject(body.Add); err != nil {
				fail(w, err)
				return
			}
			results(w, nil, nil)
			return
		}
		res, err := m.RemoveProject(body.Remove)
		results(w, res, err)
	})
	mux.HandleFunc("/api/library/backups", func(w http.ResponseWriter, r *http.Request) {
		bs, err := m.Backups()
		if err != nil {
			fail(w, err)
			return
		}
		if bs == nil {
			bs = []library.Backup{}
		}
		writeJSON(w, bs)
	})
	mux.HandleFunc("/api/library/restore", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ ID string }
		if !post(w, r, &body) {
			return
		}
		if err := m.Restore(body.ID); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/library/pick", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Title string }
		if !post(w, r, &body) {
			return
		}
		path := ""
		if PickFolder != nil {
			path = PickFolder(body.Title)
		}
		writeJSON(w, map[string]string{"path": path})
	})
	mux.HandleFunc("/api/library/reveal", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Path string }
		if !post(w, r, &body) {
			return
		}
		// only a place the library knows: the page can't open any path
		if !knownPath(m, body.Path) {
			http.Error(w, "not a library path", http.StatusBadRequest)
			return
		}
		if err := service.Reveal(body.Path); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/library/open", func(w http.ResponseWriter, r *http.Request) {
		if !post(w, r, nil) {
			return
		}
		if OpenLibrary != nil {
			OpenLibrary()
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// knownPath says whether p is one of the library's places: a target's
// folder, skills folder or MCP file, a project, or a skill in the store
// or in a target.
func knownPath(m *library.Manager, p string) bool {
	if p == "" || !filepath.IsAbs(p) {
		return false
	}
	p = filepath.Clean(p)
	st, err := m.State()
	if err != nil {
		return false
	}
	var ok []string
	for _, t := range st.Targets {
		ok = append(ok, t.Dir, t.Skills)
		if t.MCP != nil {
			ok = append(ok, t.MCP.Path)
		}
	}
	ok = append(ok, st.Projects...)
	for _, it := range st.Skills {
		ok = append(ok, it.Path)
	}
	for _, f := range st.Found {
		ok = append(ok, f.Path)
	}
	if slices.Contains(ok, p) {
		return true
	}
	// a skill folder right inside a target's skills folder
	for _, t := range st.Targets {
		if t.Skills != "" && filepath.Dir(p) == filepath.Clean(t.Skills) {
			return true
		}
	}
	return false
}
