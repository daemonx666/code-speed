//go:build !windows && !nogui

package gui

import (
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// WKWebView and WebKitGTK have no WebView2 runtime to miss and no WebView2
// process failures to handle (webview2.go).

func windowsOptions() application.WindowsOptions { return application.WindowsOptions{} }

func sysLang() string { return "" }

func webView2Prepare(string) {}

func webView2Check(*application.App, string) bool { return true }

func webView2Failed(_ *application.App, _ string, err error) { log.Println("code-speed:", err) }

func onProcessFailed(*application.WebviewWindow, func(uint32)) error { return nil }

func webViewVisible(*application.WebviewWindow, bool) error { return nil }

func reloadWebView(*application.WebviewWindow) error { return nil }
