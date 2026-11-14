package hermes

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/daemonx666/code-speed/internal/usage"
)

const maxConfig = 1 << 20

var (
	modelKeyRe   = regexp.MustCompile(`^(?:model|"model"|'model')[ \t]*:(?:[ \t]+(?:#.*)?)?$`)
	runtimeKeyRe = regexp.MustCompile(`^(?:openai_runtime|"openai_runtime"|'openai_runtime')[ \t]*:(|[ \t].*)$`)
	runtimeValRe = regexp.MustCompile(`^[ \t]+(?:"[ \t]*([A-Za-z0-9_]+)[ \t]*"|'[ \t]*([A-Za-z0-9_]+)[ \t]*'|([A-Za-z0-9_]+))(?:[ \t]+#.*)?[ \t]*$`)
)

func asciiFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// codexRuntime reports whether config.yaml selects the Codex runtime:
// model.openai_runtime is codex_app_server (4.15 Codex runtime). It is a
// tolerant line reader, not a YAML parser.
func codexRuntime(b []byte) bool {
	text := string(b)
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "�")
	}
	text = strings.TrimPrefix(text, "\xef\xbb\xbf")
	on := false
	inModel := false
	n := 0 // indentation of the model block, 0 until its first line
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSuffix(l, "\r")
		if t := strings.TrimLeft(l, " \t"); t == "" || t[0] == '#' {
			continue
		}
		ind := 0
		for ind < len(l) && l[ind] == ' ' {
			ind++
		}
		if ind == 0 {
			inModel, n = modelKeyRe.MatchString(l), 0
			continue
		}
		if !inModel {
			continue
		}
		if n == 0 {
			n = ind
		}
		if ind != n {
			continue
		}
		m := runtimeKeyRe.FindStringSubmatch(l[n:])
		if m == nil {
			continue
		}
		on = false
		if v := runtimeValRe.FindStringSubmatch(m[1]); v != nil {
			for _, g := range v[1:] {
				if g != "" && asciiFold(g, "codex_app_server") {
					on = true
				}
			}
		}
	}
	return on
}

// checkConfig rereads the log folder's config.yaml when it changed and sets
// the Codex runtime switch.
func (s *logSet) checkConfig() {
	if filepath.Base(s.dir) != "logs" {
		return
	}
	path := filepath.Join(filepath.Dir(s.dir), "config.yaml")
	st, err := os.Stat(path) // a symlink is followed
	if err != nil {
		if os.IsNotExist(err) {
			s.setCodex(false)
			s.cfgRead = false
		}
		return // another failure keeps the switch
	}
	if !st.Mode().IsRegular() {
		s.setCodex(false)
		s.cfgRead = false
		return
	}
	if s.cfgRead && st.Size() == s.cfgSize && st.ModTime().Equal(s.cfgMtime) {
		return
	}
	if st.Size() > maxConfig {
		s.setCodex(false)
		s.cfgRead, s.cfgSize, s.cfgMtime = true, st.Size(), st.ModTime()
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return // keep the switch; ask again next time
	}
	s.setCodex(len(b) <= maxConfig && codexRuntime(b))
	s.cfgRead, s.cfgSize, s.cfgMtime = true, st.Size(), st.ModTime()
}

func (s *logSet) setCodex(on bool) {
	if on && !s.codex {
		for sid, a := range s.agents {
			if a.phase == usage.Generating {
				a.phase, a.inFlight = usage.Idle, 0
				s.dirty[sid] = true
			}
		}
	}
	s.codex = on
}
