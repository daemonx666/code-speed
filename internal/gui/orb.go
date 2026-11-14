//go:build !nogui

package gui

import (
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/daemonx666/code-speed/internal/service"
	"github.com/daemonx666/code-speed/internal/settings"
)

// The orb: a small pill that floats over every window and Space with the
// live speed and today's cost. Drag it anywhere; it stays where it was
// left. Its button opens the panel.

const (
	orbW = 148
	orbH = 44
)

type orb struct {
	app  *application.App
	svc  *service.Service
	win  *application.WebviewWindow
	mu   sync.Mutex
	save *time.Timer
	// placed: it stands where it should, so its moves are the user's to
	// remember (not the system centring it when first shown)
	placed bool
}

func newOrb(app *application.App, svc *service.Service) *orb {
	o := &orb{app: app, svc: svc}
	o.win = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:           "orb",
		Title:          "Code Speed",
		Width:          orbW,
		Height:         orbH,
		Frameless:      true,
		Hidden:         true,
		AlwaysOnTop:    true,
		DisableResize:  true,
		BackgroundType: application.BackgroundTypeTransparent,
		URL:            "/orb.html",
		Mac: application.MacWindow{
			Backdrop:      application.MacBackdropTransparent,
			DisableShadow: true,
			// a true borderless window: the default keeps AppKit's frame
			// for its rounded corners, which shows as a box behind the
			// pill; the page draws the pill's own corners
			CornerType:  application.MacWindowCornerTypeSquare,
			WindowLevel: application.MacWindowLevelFloating,
			CollectionBehavior: application.MacWindowCollectionBehaviorCanJoinAllSpaces |
				application.MacWindowCollectionBehaviorFullScreenAuxiliary,
		},
		Windows: application.WindowsWindow{HiddenOnTaskbar: true},
	})
	// see makeClear: once the window exists, and again once its page has
	// loaded, in case loading turned the background back on
	clear := func() { application.InvokeAsync(func() { makeClear(o.win.NativeWindow()) }) }
	o.win.OnWindowEvent(events.Common.WindowShow, func(*application.WindowEvent) {
		clear()
		time.AfterFunc(time.Second, clear)
	})
	// remember where the user leaves it, a moment after it stops moving
	o.win.OnWindowEvent(events.Common.WindowDidMove, func(*application.WindowEvent) {
		o.mu.Lock()
		defer o.mu.Unlock()
		if o.save != nil {
			o.save.Stop()
		}
		o.save = time.AfterFunc(600*time.Millisecond, o.remember)
	})
	return o
}

func (o *orb) remember() {
	o.mu.Lock()
	placed := o.placed
	o.mu.Unlock()
	if !placed || !o.win.IsVisible() {
		return
	}
	x, y := o.win.Position()
	st := o.svc.Settings()
	if !st.Orb.On || (st.Orb.Placed && st.Orb.X == x && st.Orb.Y == y) {
		return
	}
	st.Orb.X, st.Orb.Y, st.Orb.Placed = x, y, true
	_ = o.svc.UpdateSettings(st)
}

// place moves the orb to x, y (screen points from the top left of the
// main screen, as the page computed them) and remembers it there.
func (o *orb) place(x, y int) {
	o.mu.Lock()
	o.placed = true
	o.mu.Unlock()
	application.InvokeAsync(func() { o.win.SetPosition(x, y) })
	time.AfterFunc(300*time.Millisecond, o.remember)
}

// apply shows or hides the orb as the settings say.
func (o *orb) apply(st settings.Orb) {
	application.InvokeAsync(func() {
		if !st.On {
			o.win.Hide()
			return
		}
		if o.win.IsVisible() {
			return
		}
		// where the user left it; the first time, the page moves it to the
		// top right of its screen (place), which knows that screen best
		if st.Placed {
			o.mu.Lock()
			o.placed = true
			o.mu.Unlock()
			o.win.SetPosition(st.X, st.Y)
		}
		o.win.Show()
	})
}
