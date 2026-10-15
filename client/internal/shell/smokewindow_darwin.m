//go:build darwin

// --smoke 的窗口（说明见 smokewindow_darwin.go）。

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#import <objc/message.h>

static WKWebView *dl_findWebView(NSView *view) {
	if ([view isKindOfClass:[WKWebView class]]) {
		return (WKWebView *)view;
	}
	for (NSView *sub in view.subviews) {
		WKWebView *found = dl_findWebView(sub);
		if (found != nil) {
			return found;
		}
	}
	return nil;
}

// 返回 0 成功；1 找不到 WKWebView；2 这个版本的 WebKit 没有 _setWindowOcclusionDetectionEnabled:。
int dl_prepareSmokeWindow(void *nsWindow) {
	NSWindow *window = (NSWindow *)nsWindow;
	window.alphaValue = 0.0;
	window.ignoresMouseEvents = YES;
	[window orderBack:nil];
	WKWebView *webView = dl_findWebView(window.contentView);
	if (webView == nil) {
		return 1;
	}
	SEL sel = NSSelectorFromString(@"_setWindowOcclusionDetectionEnabled:");
	if (![webView respondsToSelector:sel]) {
		return 2;
	}
	((void (*)(id, SEL, BOOL))objc_msgSend)(webView, sel, NO);
	return 0;
}
