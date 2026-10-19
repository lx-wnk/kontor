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

static void applyContentMinSize(NSWindow *window, double minWidth, double minHeight) {
	[window setContentMinSize:NSMakeSize(minWidth, minHeight)];
}

// A first launch has no saved frame; shrink the default size into the screen's visible area.
static void fitIntoVisibleFrame(NSWindow *window) {
	NSRect visible = [[window screen] ?: [NSScreen mainScreen] visibleFrame];
	if (NSIsEmptyRect(visible)) {
		return;
	}
	NSSize minFrame = [window frameRectForContentRect:NSMakeRect(0, 0, [window contentMinSize].width, [window contentMinSize].height)].size;
	NSRect frame = [window frame];
	frame.size.width = MAX(MIN(frame.size.width, visible.size.width), minFrame.width);
	frame.size.height = MAX(MIN(frame.size.height, visible.size.height), minFrame.height);
	frame.origin.x = NSMidX(visible) - frame.size.width / 2;
	frame.origin.y = NSMidY(visible) - frame.size.height / 2;
	[window setFrame:frame display:NO];
}

// Runs before wails' own queued show, so the restored frame never visibly jumps.
static void setUpMainWindow(const char *name, double designWidth, double designHeight, double minScale, double minWidth, double minHeight) {
	NSString *frameName = [NSString stringWithUTF8String:name];
	dispatch_async(dispatch_get_main_queue(), ^{
		for (NSWindow *window in [NSApp windows]) {
			if (![window isKindOfClass:NSClassFromString(@"WailsWindow")]) {
				continue;
			}
			applyContentMinSize(window, minWidth, minHeight);
			if (![window setFrameUsingName:frameName]) {
				fitIntoVisibleFrame(window);
			}
			[window setFrameAutosaveName:frameName];
			applyPageZoom(window, designWidth, designHeight, minScale);
			NSNotificationCenter *center = [NSNotificationCenter defaultCenter];
			[center addObserverForName:NSWindowDidResizeNotification object:window queue:[NSOperationQueue mainQueue]
				usingBlock:^(NSNotification *note) {
					applyPageZoom(window, designWidth, designHeight, minScale);
				}];
			// wails resets the minimum size to its own (zero) value when fullscreen ends.
			[center addObserverForName:NSWindowDidExitFullScreenNotification object:window queue:[NSOperationQueue mainQueue]
				usingBlock:^(NSNotification *note) {
					dispatch_async(dispatch_get_main_queue(), ^{
						applyContentMinSize(window, minWidth, minHeight);
					});
				}];
			return;
		}
		NSLog(@"kontor: no WailsWindow found; window frame and page zoom are not managed");
	});
}
*/
import "C"

import "unsafe"

// windowFrameName keys the main window's frame in the app's user defaults.
const windowFrameName = "KontorMainWindow"

// A window below the design size zooms the page out, down to minPageZoom; the content never shrinks past what that floor fits.
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
