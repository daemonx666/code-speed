//go:build darwin || linux

package autostart

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestOnAndOff(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	exe := "/Applications/code speed's.app/Contents/MacOS/code-speed"
	if Enabled() {
		t.Fatal("on before")
	}
	if err := Set(true, exe); err != nil {
		t.Fatal(err)
	}
	if !Enabled() {
		t.Fatal("not on")
	}
	path := desktopOrPlist()
	b, _ := os.ReadFile(path)
	want := "code speed&#39;s.app"
	if runtime.GOOS == "linux" {
		want = `Exec="/Applications/code speed's.app/Contents/MacOS/code-speed"`
	}
	if !strings.Contains(string(b), want) {
		t.Fatalf("%s:\n%s", path, b)
	}
	if err := Set(false, ""); err != nil || Enabled() {
		t.Fatalf("still on: %v", err)
	}
	if err := Set(false, ""); err != nil {
		t.Fatal("turning off twice failed")
	}
}
