package settings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/daemonx666/code-speed/internal/usage"
)

func TestNormalizeAndFormat(t *testing.T) {
	s := Settings{Language: "fr", MenuBar: "?", Theme: "x", Numbers: "roman",
		Alerts:   Alerts{Quota: []float64{80, 0, 150, 95}},
		Budget:   Budget{Daily: -1, Monthly: 50},
		Currency: Currency{Code: "XYZ", Rate: 3}}.Normalize()
	if s.Language != LangAuto || s.MenuBar != MenuSpeed || s.Theme != ThemeAuto || s.Numbers != NumbersMetric {
		t.Fatalf("choices %+v", s)
	}
	if len(s.Alerts.Quota) != 2 || s.Budget.Daily != 0 || s.Budget.Monthly != 50 || s.Currency.Code != "USD" {
		t.Fatalf("normalized %+v", s)
	}
	for _, c := range []struct {
		cur  Currency
		in   int64
		want string
	}{
		{Currency{"USD", 1}, 12_300_000, "$12.30"}, {Currency{"CNY", 7.2}, 10_000_000, "¥72.00"},
		{Currency{"JPY", 150}, 1_000_000, "¥150"}, {Currency{"USD", 1}, 5_000, "<$0.01"}, {Currency{"EUR", 0.9}, 0, "€0"},
		{Currency{"USD", 1}, 1_234_000_000, "$1234"},
	} {
		if got := c.cur.Format(c.in); got != c.want {
			t.Errorf("%v %d: %q, want %q", c.cur, c.in, got, c.want)
		}
	}
}

func TestNormalizePaths(t *testing.T) {
	home, _ := os.UserHomeDir()
	s := Default()
	s.Homes = []string{"~/mnt/box", "relative/x", " /srv/u/ ", "/srv/u"}
	s.Paths = map[usage.Source][]string{"claude-code": {"", "nope"}, "codex": {"/a/b/../c"}}
	s.Order = []usage.Source{"codex", "codex", "claude-code"}
	s = s.Normalize()
	if len(s.Homes) != 2 || s.Homes[0] != filepath.Join(home, "mnt/box") || s.Homes[1] != "/srv/u" {
		t.Fatalf("homes %q", s.Homes)
	}
	if len(s.Paths) != 1 || s.Paths["codex"][0] != "/a/c" {
		t.Fatalf("paths %q", s.Paths)
	}
	if len(s.Order) != 2 {
		t.Fatalf("order %q", s.Order)
	}
}
