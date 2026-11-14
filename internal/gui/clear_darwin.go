//go:build darwin && !nogui

package gui

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit
#import <AppKit/AppKit.h>

static void clearWebViews(NSView* v) {
	if ([v isKindOfClass:NSClassFromString(@"WKWebView")]) {
		@try { [v setValue:@NO forKey:@"drawsBackground"]; } @catch (NSException* e) {}
		if (v.layer) v.layer.backgroundColor = [NSColor clearColor].CGColor;
	}
	for (NSView* s in v.subviews) clearWebViews(s);
}

// makeClear makes a window and its webview draw nothing of their own.
static void makeClear(void* w) {
	NSWindow* win = (NSWindow*)w;
	if (win == nil) return;
	[win setOpaque:NO];
	[win setBackgroundColor:[NSColor clearColor]];
	clearWebViews(win.contentView);
}
*/
import "C"

import "unsafe"

// makeClear makes the window transparent where its page is. Wails' own
// MacBackdropTransparent leaves the webview drawing an opaque white
// background (drawsBackground stays on), which showed as a white box
// around the orb; this sets it on the webview itself. Main thread only.
func makeClear(nsWindow unsafe.Pointer) { C.makeClear(nsWindow) }
