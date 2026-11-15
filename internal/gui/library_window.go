//go:build !nogui

package gui

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// libraryWindow is the skill and MCP library's window: a regular,
// resizable window, made the first time it is opened. Closing it hides it,
// so opening it again is instant and its page keeps its place.
type libraryWindow struct {
	app *application.App
	md  *mender
	win *application.WebviewWindow
}

func newLibraryWindow(app *application.App, md *mender) *libraryWindow {
	return &libraryWindow{app: app, md: md}
}

// open shows the window and brings it forward; on the main thread.
func (l *libraryWindow) open() {
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
	l.app.Show() // a menu bar app on the Mac: come forward with the window
	l.win.Show()
	l.win.Focus()
}
