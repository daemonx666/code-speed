//go:build !nogui

package gui

import (
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// libraryWindow is the skill and MCP library's window: a regular,
// resizable window, made the first time it is opened. Closing it hides it,
// so opening it again is instant and its page keeps its place.
type libraryWindow struct {
	app *application.App
	md  *mender

	mu  sync.Mutex
	win *application.WebviewWindow
}

func newLibraryWindow(app *application.App, md *mender) *libraryWindow {
	return &libraryWindow{app: app, md: md}
}

// window answers the window, nil before it is first opened.
func (l *libraryWindow) window() *application.WebviewWindow {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.win
}

// open shows the window and brings it forward. Off the main thread (the
// tray menu's goroutine, an API request's): what must run on the main
// thread is sent there.
func (l *libraryWindow) open() {
	l.mu.Lock()
	if l.win == nil {
		l.win = l.app.Window.NewWithOptions(application.WebviewWindowOptions{
			Name:      "library",
			Title:     "Code Speed · Skill & MCP",
			Width:     980,
			Height:    680,
			MinWidth:  760,
			MinHeight: 480,
			URL:       "/library.html",
			Mac: application.MacWindow{
				TitleBar: application.MacTitleBarDefault,
			},
		})
		w := l.win
		w.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
			e.Cancel()
			w.Hide()
		})
		l.md.watch(w)
	}
	w := l.win
	l.mu.Unlock()
	application.InvokeSync(l.app.Show) // a menu bar app on the Mac: come forward with the window
	w.Show()
	w.Focus()
}
