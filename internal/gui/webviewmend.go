//go:build !nogui

package gui

import (
	"log"
	"os"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/daemonx666/code-speed/internal/update"
)

// mender handles the WebView2 failures of the app's windows (webview2.go):
// on Windows each failed process gets what it needs; elsewhere
// onProcessFailed does nothing. Ported from magpie (github.com/yetone/magpie,
// MIT, internal/gui/webviewmend_app.go).
type mender struct {
	app     *application.App
	windows []*application.WebviewWindow

	mu   sync.Mutex
	last map[*application.WebviewWindow]time.Time // each window's last reload
	// restarted is when the copy before this one asked for this one (zero:
	// it didn't); restarting is set once a restart is under way
	restarted  time.Time
	restarting bool
}

func newMender(app *application.App) *mender {
	return &mender{
		app:       app,
		last:      map[*application.WebviewWindow]time.Time{},
		restarted: lastRestart(os.Getenv(mendRestartEnv)),
	}
}

// watch has w's failures handled once its first page has come, by when
// its WebView2 is made. A window made hidden (the panel, the orb) loads its
// page all the same.
func (m *mender) watch(w *application.WebviewWindow) {
	m.windows = append(m.windows, w)
	var once sync.Once
	w.OnWindowEvent(events.Windows.WebViewNavigationCompleted, func(*application.WindowEvent) {
		once.Do(func() {
			application.InvokeAsync(func() {
				if err := onProcessFailed(w, func(kind uint32) { m.failed(w, kind) }); err != nil {
					log.Printf("webview (%s window): its process failures go unhandled: %v", w.Name(), err)
				}
			})
		})
	})
}

// failed is a failure of one of w's processes, called from WebView2's own
// callback.
func (m *mender) failed(w *application.WebviewWindow, kind uint32) {
	what, mend := webViewMendFor(kind)
	m.mu.Lock()
	now := time.Now()
	ok := true
	switch mend {
	case mendReload:
		if ok = mendAllowed(mend, m.last[w], now); ok {
			m.last[w] = now
		}
	case mendRestart:
		// a browser process that ended fails every window: one restart,
		// and not again so soon after the one that started this copy
		if m.restarting {
			m.mu.Unlock()
			return
		}
		if ok = mendAllowed(mend, m.restarted, now); ok {
			m.restarting = true
		}
	}
	m.mu.Unlock()
	if !ok {
		log.Printf("webview (%s window): %s again within %s; left as it is", w.Name(), what, mendGap)
		return
	}
	log.Printf("webview (%s window): %s; %s", w.Name(), what, mendWords[mend])
	// out of WebView2's own callback
	application.InvokeAsync(func() { m.mend(w, mend) })
}

// mend does what for w; on the main thread.
func (m *mender) mend(w *application.WebviewWindow, what webViewMend) {
	switch what {
	case mendRedraw:
		redrawShown(w)
	case mendReload:
		if err := reloadWebView(w); err != nil {
			log.Printf("webview (%s window): reload: %v", w.Name(), err)
		}
	case mendRestart:
		// the new copy waits for this one to quit, and then for the old
		// browser process to let go of the profile folder (webView2Prepare)
		if err := update.Relaunch(0, mendRestartEnv+"="+strconv.FormatInt(time.Now().Unix(), 10)); err != nil {
			log.Println("webview: starting the app again:", err)
			return
		}
		m.app.Quit()
	}
}

// redrawShown hides w's controller and shows it again, if w is open: what
// hiding it and showing it again does, without the window going. On the
// main thread.
func redrawShown(w *application.WebviewWindow) {
	if !windowOpen(w) {
		return // hidden or minimised: its showing draws it anew
	}
	if err := webViewVisible(w, false); err != nil {
		log.Printf("webview (%s window): hide: %v", w.Name(), err)
		return
	}
	// a moment, so the hide is taken as one and not folded into the show
	time.AfterFunc(100*time.Millisecond, func() {
		application.InvokeAsync(func() {
			if !windowOpen(w) {
				return
			}
			if err := webViewVisible(w, true); err != nil {
				log.Printf("webview (%s window): show: %v", w.Name(), err)
			}
		})
	})
}

func windowOpen(w *application.WebviewWindow) bool { return w.IsVisible() && !w.IsMinimised() }

// redrawOnWake draws the open windows again after the computer wakes: a GPU
// that lost its device while asleep can leave WebView2's surface white.
func (m *mender) redrawOnWake() {
	if runtime.GOOS != "windows" {
		return
	}
	m.app.Event.OnApplicationEvent(events.Common.SystemDidWake, func(*application.ApplicationEvent) {
		application.InvokeAsync(func() {
			for _, w := range m.windows {
				if windowOpen(w) {
					log.Printf("webview (%s window): the computer woke; drawing it again", w.Name())
					redrawShown(w)
				}
			}
		})
	})
}
