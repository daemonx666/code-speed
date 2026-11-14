package gui

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/daemonx666/code-speed/internal/settings"
)

func TestWebView2Missing(t *testing.T) {
	for _, c := range []struct {
		info map[string]any
		want bool
	}{
		{map[string]any{"WebView2": "131.0.2903.112"}, false},
		{map[string]any{"WebView2": ""}, true},
		{map[string]any{}, false}, // a Wails that doesn't say: assume it's there
		{nil, false},
	} {
		if got := webView2Missing(c.info); got != c.want {
			t.Errorf("webView2Missing(%v) = %v, want %v", c.info, got, c.want)
		}
	}
}

func TestWebView2Notice(t *testing.T) {
	for _, lang := range []string{settings.LangEN, settings.LangZH, "fr"} {
		title, body := webView2Notice(lang)
		if title == "" || !strings.Contains(body, webView2Page) || !strings.Contains(body, "winget install Microsoft.EdgeWebView2Runtime") {
			t.Errorf("%s: notice %q / %q lacks the page or winget", lang, title, body)
		}
	}
	if _, body := webView2Notice(settings.LangZH); !strings.Contains(body, "现在打开下载页面吗") {
		t.Errorf("zh notice not in Chinese: %q", body)
	}
	if _, body := webView2FailedNotice(settings.LangEN, "controller timeout", "131"); !strings.Contains(body, "controller timeout") || !strings.Contains(body, "131") {
		t.Errorf("failed notice lacks the reason or version: %q", body)
	}
}

func TestWebView2Folder(t *testing.T) {
	appData := filepath.Join("C:", "Users", "u", "AppData", "Roaming")
	exe := filepath.Join("C:", "Programs", "code-speed", "code-speed.exe")
	if got, want := webView2Folder("", appData, exe), filepath.Join(appData, "code-speed.exe"); got != want {
		t.Errorf("default folder = %q, want %q", got, want)
	}
	if got := webView2Folder("/x/webview2", appData, exe); got != "/x/webview2" {
		t.Errorf("set folder = %q", got)
	}
	if got := webView2Folder("", "", exe); got != "" {
		t.Errorf("no AppData: %q, want empty", got)
	}
	if got := webView2UserDataPath(""); got != "" {
		t.Errorf("no CODE_SPEED_HOME: %q, want Wails' default", got)
	}
	if got, want := webView2UserDataPath("/p"), filepath.Join("/p", "webview2"); got != want {
		t.Errorf("CODE_SPEED_HOME: %q, want %q", got, want)
	}
}

func TestWebView2Failure(t *testing.T) {
	if webView2Failure(nil) || webView2Failure(errors.New("no tray")) {
		t.Error("an error not about WebView2 read as one")
	}
	if !webView2Failure(errors.New("WebView2 controller creation timed out")) {
		t.Error("a WebView2 error not read as one")
	}
}

func TestWebView2Try(t *testing.T) {
	for v, want := range map[string]int{"": 0, "x": 0, "-1": 0, "1": 1, "2": 2} {
		if got := webView2Try(v); got != want {
			t.Errorf("webView2Try(%q) = %d, want %d", v, got, want)
		}
	}
	if webView2Backoff(1) != 2*time.Second || webView2Backoff(2) != 4*time.Second {
		t.Error("backoff not 2s per try")
	}
}

func TestAwaitWebView2Folder(t *testing.T) {
	n, slept := 0, time.Duration(0)
	sleep := func(d time.Duration) { slept += d }
	if !awaitWebView2Folder(func() bool { n++; return n >= 3 }, time.Second, 250*time.Millisecond, sleep) {
		t.Fatal("freed on the third look, but reported held")
	}
	if slept != 500*time.Millisecond {
		t.Errorf("slept %s, want 500ms", slept)
	}
	slept = 0
	if awaitWebView2Folder(func() bool { return false }, time.Second, 250*time.Millisecond, sleep) {
		t.Fatal("never free, but reported free")
	}
	if slept != time.Second {
		t.Errorf("gave up after %s, want 1s", slept)
	}
}

func TestWebViewMendFor(t *testing.T) {
	for kind, want := range map[uint32]webViewMend{
		wv2BrowserExited:      mendRestart,
		wv2RenderExited:       mendReload,
		wv2RenderUnresponsive: mendReload,
		wv2FrameRenderExited:  mendReload,
		wv2UtilityExited:      mendNothing,
		wv2GPUExited:          mendRedraw,
		wv2PPAPIPluginExited:  mendNothing,
		wv2UnknownExited:      mendRedraw,
		42:                    mendRedraw,
	} {
		if _, got := webViewMendFor(kind); got != want {
			t.Errorf("kind %d: mend %d, want %d", kind, got, want)
		}
	}
}

func TestMendAllowed(t *testing.T) {
	now := time.Unix(1000, 0)
	if !mendAllowed(mendReload, time.Time{}, now) {
		t.Error("a first reload refused")
	}
	if mendAllowed(mendReload, now.Add(-10*time.Second), now) || mendAllowed(mendRestart, now.Add(-10*time.Second), now) {
		t.Error("a reload or restart 10s after the last allowed")
	}
	if !mendAllowed(mendRestart, now.Add(-mendGap), now) {
		t.Error("a restart after the gap refused")
	}
	if !mendAllowed(mendRedraw, now, now) {
		t.Error("a redraw refused")
	}
	if !lastRestart("").IsZero() || !lastRestart("x").IsZero() {
		t.Error("no restart read as one")
	}
	if got := lastRestart("1000"); !got.Equal(now) {
		t.Errorf("lastRestart = %v", got)
	}
}

// The tray's .ico holds the disc at each size Windows asks for, each a PNG.
func TestTrayICO(t *testing.T) {
	b, err := os.ReadFile("tray-color.ico")
	if err != nil {
		t.Fatal(err)
	}
	le := binary.LittleEndian
	if len(b) < 6 || le.Uint16(b[0:]) != 0 || le.Uint16(b[2:]) != 1 {
		t.Fatal("not an .ico")
	}
	var sizes []int
	for i := range int(le.Uint16(b[4:])) {
		e := b[6+16*i:]
		size, at := int(le.Uint32(e[8:])), int(le.Uint32(e[12:]))
		if at+size > len(b) || !strings.HasPrefix(string(b[at:]), "\x89PNG") {
			t.Fatalf("image %d is not a PNG inside the file", i)
		}
		sizes = append(sizes, int(e[0]))
	}
	if want := []int{16, 20, 24, 32, 40, 48, 64}; !slices.Equal(sizes, want) {
		t.Errorf("sizes %v, want %v", sizes, want)
	}
}
