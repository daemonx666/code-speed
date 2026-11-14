//go:build !nogui

// Package gui is the desktop app: a tray icon (on macOS with the live
// speed or today's cost beside it) and a panel that drops down from it.
// The panel is plain HTML in the system webview (Wails v3).
package gui

import (
	"context"
	_ "embed"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"

	"github.com/daemonx666/code-speed/internal/autostart"
	"github.com/daemonx666/code-speed/internal/cli"
	"github.com/daemonx666/code-speed/internal/service"
	"github.com/daemonx666/code-speed/internal/settings"
	"github.com/daemonx666/code-speed/internal/status"
	"github.com/daemonx666/code-speed/internal/update"
)

//go:embed tray.png
var trayTemplate []byte // black glyph; macOS tints it for the menu bar

//go:embed tray-color.png
var trayColor []byte // Linux

// trayColorICO is the same disc drawn at each size Windows asks a tray
// icon for (16px at 100% scaling up to 64): Wails picks the one it needs,
// where one PNG was scaled down by the system and blurred.
//
//go:embed tray-color.ico
var trayColorICO []byte

//go:embed icon.png
var appIcon []byte

// the tray menu's words
var menuText = map[string][3]string{
	settings.LangEN: {"Open Code Speed", "Quit", "Floating speed orb"},
	settings.LangZH: {"打开 Code Speed", "退出", "悬浮球"},
}

// instanceID is the single instance lock's id.
const instanceID = "io.github.daemonx666.code-speed"

// Run starts the app and blocks until it quits.
func Run(version string) error {
	svc := service.New(version)
	ns := notifications.New()

	var (
		app      *application.App
		tray     *application.SystemTray
		openItem *application.MenuItem
		quitItem *application.MenuItem
		orbItem  *application.MenuItem
		mu       sync.Mutex
		pageLang = os.Getenv("LANG") // until the panel tells its own
	)
	if pageLang == "" {
		pageLang = sysLang() // Windows sets no LANG
	}
	lang := func() string {
		mu.Lock()
		defer mu.Unlock()
		return Lang(svc.Settings(), pageLang)
	}
	relabel := func() {
		t := menuText[lang()]
		application.InvokeAsync(func() {
			openItem.SetLabel(t[0])
			orbItem.SetLabel(t[2])
			quitItem.SetLabel(t[1])
		})
	}

	// a start right after another waits for its WebView2 to let go
	webView2Prepare(instanceID)
	app = application.New(application.Options{
		Name:        "code-speed",
		Description: "Live token speed, usage, cost and quotas of your coding agents",
		Icon:        appIcon,
		Assets: application.AssetOptions{Handler: Handler(svc, func() { app.Quit() }, func(l string) {
			mu.Lock()
			pageLang = l
			mu.Unlock()
			relabel()
		})},
		Mac:     application.MacOptions{ActivationPolicy: application.ActivationPolicyAccessory},
		Windows: windowsOptions(),
		// Wails exits on some webview errors; say why before it does, and
		// on Windows try a start that failed again
		ErrorHandler: func(err error) { webView2Failed(app, lang(), err) },
		Services:     []application.Service{application.NewService(ns)},
		// a second launch (the app opened again, or the login item firing
		// while it runs) opens this one's panel and quits, before it has
		// read a log or written a file
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: instanceID,
			OnSecondInstanceLaunch: func(application.SecondInstanceData) {
				application.InvokeAsync(func() {
					if tray != nil {
						tray.ShowWindow()
					}
				})
			},
		},
	})

	// Windows without the WebView2 runtime can make no window: Wails would
	// exit on the first, with nothing on screen to say why. A box of
	// Windows' own says so instead, and the launch ends.
	if !webView2Check(app, lang()) {
		return nil
	}

	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		svc.HelperPath = exe
		update.CleanOld(exe) // what a portable Windows update left
		if err := svc.RepairBridge(); err != nil {
			log.Println("claude status line:", err)
		}
		// the app moved or was updated: point the login item at it again
		// (this also drops a login item of the project's old name)
		if svc.Settings().LaunchAtLogin {
			if err := autostart.Set(true, exe); err != nil {
				log.Println("start at login:", err)
			}
		} else if autostart.HadOld() {
			_ = autostart.Set(false, "")
		}
	}
	dropOldBundleData()
	svc.Start()
	defer svc.Stop()

	tray = app.SystemTray.New()
	if runtime.GOOS == "darwin" {
		tray.SetTemplateIcon(trayTemplate)
	} else if runtime.GOOS == "windows" {
		tray.SetIcon(trayColorICO)
	} else {
		tray.SetIcon(trayColor)
	}
	tray.SetTooltip("Code Speed")

	panel := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:            "panel",
		Title:           "Code Speed",
		Width:           400,
		Height:          660,
		Frameless:       true,
		Hidden:          true,
		AlwaysOnTop:     true,
		DisableResize:   true,
		HideOnEscape:    true,
		HideOnFocusLost: true,
		URL:             "/",
		Windows:         application.WindowsWindow{HiddenOnTaskbar: true},
	})
	tray.AttachWindow(panel).WindowOffset(6)

	t := menuText[Lang(svc.Settings(), pageLang)]
	menu := app.NewMenu()
	openItem = menu.Add(t[0]).OnClick(func(*application.Context) { tray.ShowWindow() })
	OpenPanel = func() { application.InvokeAsync(tray.ShowWindow) }
	ob := newOrb(app, svc)
	PlaceOrb = ob.place
	// WebView2's failed processes are mended, not left white (Windows)
	md := newMender(app)
	md.watch(panel)
	md.watch(ob.win)
	md.redrawOnWake()
	orbItem = menu.AddCheckbox(t[2], svc.Settings().Orb.On).OnClick(func(*application.Context) {
		st := svc.Settings()
		st.Orb.On = !st.Orb.On
		if err := svc.UpdateSettings(st); err != nil {
			log.Println("orb:", err)
		}
	})
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		ob.apply(svc.Settings().Orb)
	})
	// an update: start the command that waits for this process to quit
	// and opens the new copy, then quit
	svc.Restart = func(cmd *exec.Cmd) {
		if err := cmd.Start(); err != nil {
			log.Println("update:", err)
			return
		}
		app.Quit()
	}
	menu.AddSeparator()
	quitItem = menu.Add(t[1]).OnClick(func(*application.Context) { app.Quit() })
	tray.SetMenu(menu)
	// the global shortcut opens and closes the panel
	var shortcut string
	bindShortcut := func(st settings.Settings) {
		if shortcut == st.Shortcut {
			return
		}
		if shortcut != "" {
			app.GlobalShortcut.Unregister(shortcut)
		}
		shortcut = ""
		if st.Shortcut != "" {
			if err := app.GlobalShortcut.Register(st.Shortcut, func() { tray.ToggleWindow() }); err != nil {
				log.Println("shortcut:", err)
				return
			}
			shortcut = st.Shortcut
		}
	}
	bindShortcut(svc.Settings())
	lastStatusCheck := svc.Settings().StatusCheck
	svc.OnSettings = func(st settings.Settings) {
		ob.apply(st.Orb)
		application.InvokeAsync(func() { orbItem.SetChecked(st.Orb.On) })
		if st.StatusCheck && !lastStatusCheck {
			go svc.CheckStatus(context.Background(), true)
		}
		lastStatusCheck = st.StatusCheck
		relabel()
		bindShortcut(st)
		go svc.CheckUpdate(context.Background())
		if st.PriceSync && svc.PriceSyncDue(time.Now()) {
			go svc.SyncPrices(context.Background(), false)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// update checks: at start and once a day, when the user allowed them
	go func() {
		for {
			svc.CheckUpdate(ctx)
			select {
			case <-ctx.Done():
				return
			case <-time.After(24 * time.Hour):
			}
		}
	}()
	// the price table: a minute after start, then daily (checked hourly),
	// when the user allowed it
	go func() {
		wait := time.Minute
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait = time.Hour
			if svc.Settings().PriceSync && svc.PriceSyncDue(time.Now()) {
				if _, err := svc.SyncPrices(ctx, false); err != nil {
					log.Println("prices:", err)
				}
			}
		}
	}()
	// the providers' status pages: every 10 minutes, when allowed; a
	// provider that fails waits longer (status.Checker backs off)
	go func() {
		wait := 5 * time.Second
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait = status.Every
			svc.CheckStatus(ctx, false)
		}
	}()
	notifyOK := false
	go func() {
		ok, err := ns.RequestNotificationAuthorization()
		if err != nil {
			log.Println("notifications:", err)
		}
		mu.Lock()
		notifyOK = ok
		mu.Unlock()
	}()
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		last := ""
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			if !svc.Ready() {
				continue
			}
			s := svc.Monitor().Engine.Snapshot(time.Now().UnixMilli())
			label := TrayText(svc.Settings().MenuBar, s, cli.Speed, svc.Settings().Currency.Format)
			// SetLabel goes through the main thread; only when it changes
			if label != last {
				tray.SetLabel(label) // macOS only; a no-op elsewhere
				last = label
			}
			tray.SetTooltip(cli.Line(s))

			mu.Lock()
			canNotify := notifyOK
			mu.Unlock()
			for _, a := range svc.Alerts().Check(s, svc.Settings(), lang()) {
				if !canNotify {
					continue // still in the panel's list
				}
				if err := ns.SendNotification(notifications.NotificationOptions{ID: a.Key, Title: a.Title, Body: a.Body}); err != nil {
					log.Println("notify:", err)
				}
			}
		}
	}()

	return app.Run()
}

// oldBundleIDs are the macOS bundle ids of earlier builds, from before the
// project was renamed.
var oldBundleIDs = []string{"io.github.felix-xqs.code-speed"}

// dropOldBundleData removes what macOS kept for the app under an old
// bundle id: the webview's storage and caches, under 1 MB, nothing the
// new id can read.
func dropOldBundleData() {
	if runtime.GOOS != "darwin" {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	for _, id := range oldBundleIDs {
		for _, dir := range []string{"WebKit", "Caches", "HTTPStorages"} {
			_ = os.RemoveAll(filepath.Join(home, "Library", dir, id))
		}
	}
}
