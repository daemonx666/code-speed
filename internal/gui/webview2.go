package gui

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/daemonx666/code-speed/internal/settings"
)

// The Windows app draws its panel and orb with WebView2, a runtime that
// Windows 10 doesn't always have and whose processes can fail on their own.
// What follows is the platform-free part of handling that; the calls into
// Windows and WebView2 are in webview2_windows.go. Ported from magpie
// (github.com/yetone/magpie, MIT, internal/gui/webview2.go and
// webviewmend.go), where each of these was a user's report.

// webView2Page is Microsoft's download page for the runtime.
const webView2Page = "https://developer.microsoft.com/microsoft-edge/webview2/"

// releasesPage is where the terminal-only build is downloaded.
const releasesPage = "https://github.com/daemonx666/code-speed/releases"

// webView2Missing reads Wails' own answer, Env.Info().PlatformInfo's
// "WebView2": the runtime's version, "" when its lookup failed. A key Wails
// doesn't set at all says nothing and is read as there: refusing to start
// an app that would have worked is worse than the silent exit.
func webView2Missing(info map[string]any) bool {
	v, ok := info["WebView2"]
	return ok && fmt.Sprint(v) == ""
}

// webView2Notice is the box shown when the runtime is missing, in lang
// (Lang's), English where it has none: where to get the runtime, and
// whether to open that page now (Yes opens it).
func webView2Notice(lang string) (title, body string) {
	if lang == settings.LangZH {
		return "Code Speed 需要 WebView2",
			"Code Speed 的面板需要 Microsoft Edge WebView2 运行时，这台电脑上没有安装。\r\n\r\n" +
				"请从这里安装：\r\n" + webView2Page + "\r\n\r\n或在终端里运行：\r\nwinget install Microsoft.EdgeWebView2Runtime\r\n\r\n" +
				"装好之前，可以从 " + releasesPage + " 下载终端版（code-speed-cli-…-windows 压缩包），运行 code-speed watch 查看实时速度。\r\n\r\n" +
				"现在打开下载页面吗？"
	}
	return "Code Speed needs WebView2",
		"Code Speed's panel needs Microsoft Edge WebView2 Runtime, which isn't installed on this computer.\r\n\r\n" +
			"Install it from:\r\n" + webView2Page + "\r\n\r\nor in a terminal:\r\nwinget install Microsoft.EdgeWebView2Runtime\r\n\r\n" +
			"Until then, the terminal version (the code-speed-cli-…-windows zip at " + releasesPage + ") shows the live speed with code-speed watch.\r\n\r\n" +
			"Open the download page now?"
}

// webView2FailedNotice is the box shown when the webview would not start
// after every try: what failed and what to do.
func webView2FailedNotice(lang, reason, version string) (title, body string) {
	if lang == settings.LangZH {
		return "Code Speed 的面板没能打开",
			fmt.Sprintf("WebView2 在 %d 次启动里都没有就绪（运行时 %s）。\r\n\r\n%s\r\n\r\n请在任务管理器里结束残留的 msedgewebview2.exe 后重新打开 Code Speed。", webView2Tries, version, reason)
	}
	return "Code Speed's panel didn't open",
		fmt.Sprintf("WebView2 was not ready in %d starts (runtime %s).\r\n\r\n%s\r\n\r\nEnd any leftover msedgewebview2.exe in Task Manager and open Code Speed again.", webView2Tries, version, reason)
}

// webViewOcclusion is Chromium's tracking of whether a window is covered,
// turned off: when it is wrong, as for a window shown while the screen is
// locked or the display off, the page stays white. A hidden or minimised
// window is told so by Wails (put_IsVisible) without it.
const webViewOcclusion = "CalculateNativeWinOcclusion"

// webView2UserDataPath is the profile folder handed to Wails: "" (Wails'
// own %AppData%\code-speed.exe, which the installer's uninstall removes and
// the pages' storage already lives in) unless CODE_SPEED_HOME puts
// code-speed's files elsewhere, when the profile goes there too so a
// portable copy keeps everything in one folder.
func webView2UserDataPath(home string) string {
	if home == "" {
		return ""
	}
	return filepath.Join(home, "webview2")
}

// webView2Folder is the folder WebView2 keeps its profile in: userDataPath
// when set, else Wails' default, %AppData%\<exe name>.
func webView2Folder(userDataPath, appData, exe string) string {
	if userDataPath != "" {
		return userDataPath
	}
	if appData == "" || exe == "" {
		return ""
	}
	return filepath.Join(appData, filepath.Base(exe))
}

// A start right after another (an update, a restart) finds the profile
// folder still held by the old msedgewebview2, and Wails waits for a
// controller that never comes, then exits. Two things answer it: the start
// waits for the folder to be free, and a start that still fails is tried
// again, a few times.

// webView2TryEnv counts the starts that ended in a WebView2 failure; each
// relaunch passes it on.
const webView2TryEnv = "CODE_SPEED_WV2_TRY"

// webView2Tries is how many starts are made before the failure is shown.
const webView2Tries = 3

// webView2Failure says whether err is the webview not coming up: the
// errors Wails reports for it all name WebView2.
func webView2Failure(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "webview2")
}

// webView2Try is the number of starts already made, from the env value.
func webView2Try(v string) int {
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// webView2Backoff is how long a start waits before the n-th try (n >= 1).
func webView2Backoff(n int) time.Duration { return time.Duration(n) * 2 * time.Second }

// awaitWebView2Folder polls free until it says so or max passes, and says
// whether it was free. sleep is a parameter for tests.
func awaitWebView2Folder(free func() bool, max, step time.Duration, sleep func(time.Duration)) bool {
	for waited := time.Duration(0); ; waited += step {
		if free() {
			return true
		}
		if waited >= max {
			return false
		}
		sleep(step)
	}
}

// A WebView2 whose process fails is left as it fell: nothing in Wails
// handles ProcessFailed. In magpie a window went white but for its edges,
// with nothing in the log, until it was closed and opened again. Each
// failure now gets what it needs (webViewMendFor), and is logged.

// webViewMend is what is done for a failed WebView2 process.
type webViewMend int

const (
	mendNothing webViewMend = iota
	// mendRedraw hides the controller and shows it again, which has it
	// draw its surface anew: WebView2 starts a new GPU process itself, but
	// a window that stays shown may keep the surface the old one drew.
	mendRedraw
	// mendReload reloads the page: a renderer that ended is replaced with
	// an error page, one that hangs draws nothing more.
	mendReload
	// mendRestart starts the app again: a browser process that ended
	// closes every webview for good.
	mendRestart
)

// COREWEBVIEW2_PROCESS_FAILED_KIND's values (WebView2.h).
const (
	wv2BrowserExited       = 0
	wv2RenderExited        = 1
	wv2RenderUnresponsive  = 2
	wv2FrameRenderExited   = 3
	wv2UtilityExited       = 4
	wv2SandboxHelperExited = 5
	wv2GPUExited           = 6
	wv2PPAPIPluginExited   = 7
	wv2PPAPIBrokerExited   = 8
	wv2UnknownExited       = 9
)

// webViewMendFor names a ProcessFailed kind and says what is done for it.
// A process whose end leaves the page as it was needs nothing; an unknown
// one gets a redraw, which costs nothing.
func webViewMendFor(kind uint32) (string, webViewMend) {
	switch kind {
	case wv2BrowserExited:
		return "browser process exited", mendRestart
	case wv2RenderExited:
		return "render process exited", mendReload
	case wv2RenderUnresponsive:
		return "render process unresponsive", mendReload
	case wv2FrameRenderExited:
		return "a frame's render process exited", mendReload
	case wv2UtilityExited:
		return "utility process exited", mendNothing
	case wv2SandboxHelperExited:
		return "sandbox helper process exited", mendNothing
	case wv2GPUExited:
		return "GPU process exited", mendRedraw
	case wv2PPAPIPluginExited, wv2PPAPIBrokerExited:
		return "plugin process exited", mendNothing
	case wv2UnknownExited:
		return "a process exited", mendRedraw
	}
	return fmt.Sprintf("process failed (kind %d)", kind), mendRedraw
}

// mendGap is how soon after a reload or a restart another is not made: a
// page that brings its renderer down again as it loads would otherwise be
// reloaded without end. The failure is still logged.
const mendGap = 30 * time.Second

// mendRestartEnv is set on a copy started by mendRestart, to the Unix time
// it was asked for; that copy restarts again only once mendGap has passed.
const mendRestartEnv = "CODE_SPEED_WV2_RESTARTED"

// mendAllowed says whether a mend may run at now, the last heavy one
// (reload or restart) having run at last (zero: never). A redraw always may.
func mendAllowed(m webViewMend, last, now time.Time) bool {
	if m != mendReload && m != mendRestart {
		return true
	}
	return last.IsZero() || now.Sub(last) >= mendGap
}

// lastRestart is when the copy before this one asked for this one to be
// started (mendRestartEnv), zero when it wasn't.
func lastRestart(v string) time.Time {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

// mendWords say each mend in the log.
var mendWords = map[webViewMend]string{
	mendNothing: "nothing to do",
	mendRedraw:  "drawing it again",
	mendReload:  "reloading the page",
	mendRestart: "starting the app again",
}
