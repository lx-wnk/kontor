//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#include <stdlib.h>

static double kontorPageZoom(double width, double height, double designWidth, double designHeight, double minScale) {
	double scale = MIN(width / designWidth, height / designHeight);
	return MIN(1.0, MAX(minScale, scale));
}

static WKWebView *findWebView(NSView *view) {
	if ([view isKindOfClass:[WKWebView class]]) {
		return (WKWebView *)view;
	}
	for (NSView *child in [view subviews]) {
		WKWebView *found = findWebView(child);
		if (found != nil) {
			return found;
		}
	}
	return nil;
}

static void applyPageZoom(NSWindow *window, double designWidth, double designHeight, double minScale) {
	WKWebView *webView = findWebView([window contentView]);
	if (webView == nil) {
		return;
	}
	NSSize size = [[window contentView] bounds].size;
	webView.pageZoom = kontorPageZoom(size.width, size.height, designWidth, designHeight, minScale);
}

// Wails builds its window before OnStartup but shows it only from the run loop,
// so restoring here, on the main queue and before ordering it front, avoids a
// visible jump. macOS clamps the saved frame to the attached screens itself.
static void setUpMainWindow(const char *name, double designWidth, double designHeight, double minScale, double minWidth, double minHeight) {
	NSString *frameName = [NSString stringWithUTF8String:name];
	dispatch_async(dispatch_get_main_queue(), ^{
		for (NSWindow *window in [NSApp windows]) {
			if (![window isKindOfClass:NSClassFromString(@"WailsWindow")]) {
				continue;
			}
			[window setContentMinSize:NSMakeSize(minWidth, minHeight)];
			[window setFrameUsingName:frameName];
			[window setFrameAutosaveName:frameName];
			applyPageZoom(window, designWidth, designHeight, minScale);
			[[NSNotificationCenter defaultCenter] addObserverForName:NSWindowDidResizeNotification
				object:window
				queue:[NSOperationQueue mainQueue]
				usingBlock:^(NSNotification *note) {
					applyPageZoom(window, designWidth, designHeight, minScale);
				}];
			[window makeKeyAndOrderFront:nil];
			return;
		}
	});
}
*/
import "C"

import "unsafe"

// windowFrameName keys the main window's frame in the app's user defaults.
const windowFrameName = "KontorMainWindow"

// The layout is designed for designWidth×designHeight points; a smaller window
// zooms the page out to fit, down to minPageZoom. The content area cannot shrink
// past the size that floor still fits, so nothing is ever cut off.
const (
	designWidth  = 1400
	designHeight = 900
	minPageZoom  = 0.65
	minWidth     = int(designWidth * minPageZoom)
	minHeight    = int(designHeight * minPageZoom)
)

func setUpMainWindow() {
	name := C.CString(windowFrameName)
	defer C.free(unsafe.Pointer(name))
	C.setUpMainWindow(name, designWidth, designHeight, minPageZoom, C.double(minWidth), C.double(minHeight))
}

func pageZoomFor(width, height float64) float64 {
	return float64(C.kontorPageZoom(C.double(width), C.double(height), designWidth, designHeight, minPageZoom))
}
