//go:build darwin

// 导航保护的 Objective-C 部分，说明见 navguard_darwin.go。

#import <Foundation/Foundation.h>
#import <WebKit/WebKit.h>
#import <objc/runtime.h>

// Go 端（navguard_darwin.go）导出。
extern void docliteNavigationBlocked(char *url, int mainFrame);

// 页面只能停在壳的资产服务器（wails://localhost，开发构建接 vite 时带端口）上：嵌入的界面和客户端自带的页面（/shell/）
// 都在这里；about:blank、about:srcdoc 给 iframe 用。其他一律拦下（跳到服务器的地址、外部网站、wails://别的主机、
// file:// 都不行）。下载、外部链接由页面调 open_external 交给系统浏览器。
static void dl_decidePolicy(id self, SEL _cmd, WKWebView *webView, WKNavigationAction *action,
                            void (^decisionHandler)(WKNavigationActionPolicy)) {
	NSURL *url = action.request.URL;
	NSString *scheme = url.scheme.lowercaseString;
	BOOL allow = ([scheme isEqualToString:@"wails"] && [url.host.lowercaseString isEqualToString:@"localhost"]) ||
	             [scheme isEqualToString:@"about"];
	if (!allow) {
		BOOL mainFrame = action.targetFrame == nil || action.targetFrame.isMainFrame;
		const char *s = url.absoluteString.UTF8String;
		docliteNavigationBlocked((char *)(s ? s : ""), mainFrame ? 1 : 0);
	}
	decisionHandler(allow ? WKNavigationActionPolicyAllow : WKNavigationActionPolicyCancel);
}

// 给 Wails 的窗口代理类（WebviewWindowDelegate，它是 WKWebView 的 navigationDelegate，自己没有实现
// decidePolicyForNavigationAction）补上导航决策。要在建窗口之前调用：WebKit 在设置 navigationDelegate 时
// 记下代理实现了哪些方法。返回 0 成功，1 找不到类，2 类已经实现了这个方法（Wails 升级后要重新看）。
int dl_installNavigationGuard(void) {
	Class cls = objc_getClass("WebviewWindowDelegate");
	if (cls == nil) {
		return 1;
	}
	SEL sel = @selector(webView:decidePolicyForNavigationAction:decisionHandler:);
	if (!class_addMethod(cls, sel, (IMP)dl_decidePolicy, "v@:@@@?")) {
		return 2;
	}
	return 0;
}
