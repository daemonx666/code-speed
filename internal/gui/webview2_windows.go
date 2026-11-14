//go:build windows && !nogui

package gui

import (
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"golang.org/x/sys/windows"

	"github.com/daemonx666/code-speed/internal/update"
)

// The calls into Windows and WebView2 behind webview2.go. Ported from
// magpie (github.com/yetone/magpie, MIT, internal/gui/webview2_windows.go,
// webviewmend_windows.go and memtarget_windows.go). The ProcessFailed hook
// and the COM calls reach into Wails' unexported edge.Chromium by
// reflection, as Wails v3.0.0-beta.24 lays it out; a Wails laid out
// otherwise is told apart and logged, not crashed on.

// windowsOptions are the app's Windows options: the profile folder
// (webView2UserDataPath) and Chromium's occlusion tracking off.
func windowsOptions() application.WindowsOptions {
	return application.WindowsOptions{
		WebviewUserDataPath: webView2UserDataPath(os.Getenv("CODE_SPEED_HOME")),
		DisabledFeatures:    []string{webViewOcclusion},
	}
}

// sysLang is Windows' display language ("zh-CN"), "" when it can't be
// read: Windows sets no LANG.
func sysLang() string {
	langs, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	if err != nil || len(langs) == 0 {
		return ""
	}
	return langs[0]
}

// webView2Check says whether the app's windows can be made. Without the
// runtime it logs it and shows Windows' own box, opening the download page
// on Yes, and the app should end: Wails would exit on the first window
// anyway, with nothing on screen (the desktop build has no console). It
// reads the registry and makes no window, so it runs before app.Run.
func webView2Check(app *application.App, lang string) bool {
	if !webView2Missing(app.Env.Info().PlatformInfo) {
		return true
	}
	log.Printf("code-speed: no Microsoft Edge WebView2 Runtime, which the panel needs; install it from %s", webView2Page)
	title, body := webView2Notice(lang)
	if messageBox(title, body, mbYesNo|mbIconError|mbSetForeground|mbTopmost) == idYes {
		if err := windows.ShellExecute(0, windows.StringToUTF16Ptr("open"), windows.StringToUTF16Ptr(webView2Page), nil, nil, windows.SW_SHOWNORMAL); err != nil {
			log.Println("code-speed: opening the WebView2 page:", err)
		}
	}
	return false
}

const (
	mbYesNo         = 0x00000004
	mbIconError     = 0x00000010
	mbSetForeground = 0x00010000
	mbTopmost       = 0x00040000
	idYes           = 6
)

var procMessageBoxW = windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW")

// messageBox is Windows' own message box, answering the button pressed.
func messageBox(title, body string, style uintptr) int {
	r, _, _ := procMessageBoxW.Call(0,
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(body))),
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(title))),
		style)
	return int(r)
}

// webView2Dir is the profile folder WebView2 will use.
func webView2Dir() string {
	exe, _ := os.Executable()
	return webView2Folder(webView2UserDataPath(os.Getenv("CODE_SPEED_HOME")), os.Getenv("AppData"), exe)
}

// webView2Free says whether no other process holds the profile folder.
// Chromium keeps EBWebView\lockfile open without sharing while its browser
// process runs, so opening it fails with a sharing violation until the
// previous copy's msedgewebview2 has gone.
func webView2Free(dir string) bool {
	if dir == "" {
		return true
	}
	f, err := os.OpenFile(filepath.Join(dir, "EBWebView", "lockfile"), os.O_RDWR, 0)
	if err != nil {
		return !errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	f.Close()
	return true
}

// webView2Prepare runs before the app is made. A start after a failed one
// backs off first; then it waits, up to 30s, for the previous copy's
// WebView2 to let go of the profile folder. A second launch waits for
// nothing: the copy running holds the folder for as long as it runs, and
// Wails hands the launch over to it as the app is made.
func webView2Prepare(instanceID string) {
	if try := webView2Try(os.Getenv(webView2TryEnv)); try > 0 {
		log.Printf("code-speed: WebView2 start %d of %d, after a failure", try+1, webView2Tries)
		time.Sleep(webView2Backoff(try))
	}
	if instanceHeld(instanceID) {
		return
	}
	dir, start, max := webView2Dir(), time.Now(), 30*time.Second
	if !awaitWebView2Folder(func() bool { return webView2Free(dir) }, max, 250*time.Millisecond, time.Sleep) {
		log.Printf("code-speed: WebView2 folder %s is still held by another process after %s; going on", dir, max)
	} else if d := time.Since(start); d > time.Second {
		log.Printf("code-speed: waited %s for WebView2 folder %s to be free", d.Round(100*time.Millisecond), dir)
	}
}

// instanceHeld says whether a copy with the single instance id runs: Wails
// holds a mutex named for it from the app's making until the process ends
// (its single_instance_windows.go).
func instanceHeld(id string) bool {
	h, err := windows.OpenMutex(windows.SYNCHRONIZE, false, windows.StringToUTF16Ptr("wails-app-"+id+"-sim"))
	if err != nil {
		return false
	}
	windows.CloseHandle(h)
	return true
}

// webView2Failed is Wails' ErrorHandler; Wails exits as soon as it returns
// on a fatal one. It logs the error with what is needed to read it, and
// for a WebView2 failure starts the app again (up to webView2Tries starts),
// the last failure shown in a box.
func webView2Failed(app *application.App, lang string, err error) {
	dir := webView2Dir()
	ver := "unknown"
	if app != nil {
		ver = fmt.Sprint(app.Env.Info().PlatformInfo["WebView2"])
	}
	log.Printf("code-speed: %v (WebView2 runtime %q, profile folder %s, held by another process: %t)", err, ver, dir, !webView2Free(dir))
	if !webView2Failure(err) {
		return
	}
	try := webView2Try(os.Getenv(webView2TryEnv)) + 1
	if try < webView2Tries {
		log.Printf("code-speed: starting again (start %d of %d)", try+1, webView2Tries)
		// Wails sends errors it goes on after here too, some naming
		// WebView2: the new copy starts only if this one has exited
		if rerr := update.Relaunch(15*time.Second, fmt.Sprintf("%s=%d", webView2TryEnv, try)); rerr == nil {
			return
		} else {
			log.Println("code-speed: starting again:", rerr)
		}
	}
	title, body := webView2FailedNotice(lang, err.Error(), ver)
	messageBox(title, body, mbIconError|mbSetForeground|mbTopmost)
}

// onProcessFailed has f called with the kind of each failure of w's
// WebView2 processes (COREWEBVIEW2_PROCESS_FAILED_KIND). Wails' Chromium
// registers for ProcessFailed and calls its ProcessFailedCallback, which
// Wails itself leaves unset. On the main thread, once w's WebView2 is made.
func onProcessFailed(w *application.WebviewWindow, f func(kind uint32)) error {
	c := chromiumStruct(w)
	if !c.IsValid() {
		return errors.New("no WebView2 found in this Wails")
	}
	cb := c.FieldByName("ProcessFailedCallback")
	if !cb.IsValid() || cb.Kind() != reflect.Func || cb.Type().NumIn() != 2 || cb.Type().NumOut() != 0 ||
		cb.Type().In(1).Kind() != reflect.Pointer {
		return errors.New("Wails' Chromium has no ProcessFailedCallback of the shape known")
	}
	if !cb.IsNil() {
		return errors.New("Wails handles ProcessFailed itself now")
	}
	cb.Set(reflect.MakeFunc(cb.Type(), func(in []reflect.Value) []reflect.Value {
		kind := uint32(math.MaxUint32)
		if a := in[1]; !a.IsNil() {
			kind = processFailedKind(a.UnsafePointer())
		}
		f(kind)
		return nil
	}))
	return nil
}

// processFailedKind is ICoreWebView2ProcessFailedEventArgs'
// get_ProcessFailedKind (slot 3, after IUnknown's), MaxUint32 when it
// fails.
func processFailedKind(args unsafe.Pointer) uint32 {
	kind := uint32(math.MaxUint32)
	if hr, _, _ := syscall.SyscallN(comMethod(args, 3), uintptr(args), uintptr(unsafe.Pointer(&kind))); int32(hr) < 0 {
		return math.MaxUint32
	}
	return kind
}

// webViewVisible is ICoreWebView2Controller's put_IsVisible (slot 4:
// IUnknown's three, then get_IsVisible), as Wails' own Show and Hide call
// it; on the main thread.
func webViewVisible(w *application.WebviewWindow, visible bool) error {
	c := chromiumPart(w, "controller")
	if c == nil {
		return errors.New("no controller")
	}
	var v uintptr
	if visible {
		v = 1
	}
	if hr, _, _ := syscall.SyscallN(comMethod(c, 4), uintptr(c), v); int32(hr) < 0 {
		return fmt.Errorf("put_IsVisible(%t): %w", visible, windows.Errno(hr))
	}
	return nil
}

// reloadWebView is ICoreWebView2's Reload (slot 31 in ICoreWebView2Vtbl,
// WebView2.h), which loads the page again in a new renderer; on the main
// thread.
func reloadWebView(w *application.WebviewWindow) error {
	wv := chromiumPart(w, "webview")
	if wv == nil {
		return errors.New("no webview")
	}
	const reload = 31
	if hr, _, _ := syscall.SyscallN(comMethod(wv, reload), uintptr(wv)); int32(hr) < 0 {
		return fmt.Errorf("Reload: %w", windows.Errno(hr))
	}
	return nil
}

// comMethod is the slot'th method of the COM object at p's vtable.
func comMethod(p unsafe.Pointer, slot uintptr) uintptr {
	vtbl := *(*unsafe.Pointer)(p)
	return *(*uintptr)(unsafe.Add(vtbl, slot*unsafe.Sizeof(uintptr(0))))
}

// chromiumField walks w.impl.chromium (an *edge.Chromium in Wails
// v3.0.0-beta.24) and then path, each a pointer or an interface holding
// one; the zero Value before WebView2 is made or in a Wails without that
// path.
func chromiumField(w *application.WebviewWindow, path ...string) reflect.Value {
	if w == nil {
		return reflect.Value{}
	}
	v := reflect.ValueOf(w)
	for _, name := range append([]string{"impl", "chromium"}, path...) {
		v = v.Elem()
		if v.Kind() != reflect.Struct {
			return reflect.Value{}
		}
		v = v.FieldByName(name)
		for v.IsValid() && v.Kind() == reflect.Interface {
			if v.IsNil() {
				return reflect.Value{}
			}
			v = v.Elem()
		}
		if !v.IsValid() || v.Kind() != reflect.Pointer || v.IsNil() {
			return reflect.Value{}
		}
	}
	return v
}

// chromiumPart is one of the COM objects Wails' Chromium holds for w
// ("controller", "webview"), nil before WebView2 has made it.
func chromiumPart(w *application.WebviewWindow, part string) unsafe.Pointer {
	v := chromiumField(w, part)
	if !v.IsValid() {
		return nil
	}
	return v.UnsafePointer()
}

// chromiumStruct is Wails' edge.Chromium for w as a value whose fields can
// be set.
func chromiumStruct(w *application.WebviewWindow) reflect.Value {
	v := chromiumField(w)
	if !v.IsValid() || v.Elem().Kind() != reflect.Struct {
		return reflect.Value{}
	}
	// reached through unexported fields, v can't be set; the same memory
	// taken afresh can
	return reflect.NewAt(v.Elem().Type(), v.UnsafePointer()).Elem()
}
