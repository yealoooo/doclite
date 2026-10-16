// Package webcsp 是壳里页面的内容安全策略（CSP）。打包的壳（internal/shell）给资产服务器的每个响应加上它；
// 截图脚本用的开发服务器（cmd/devshell）给"壳模式"的页面加同一份。
package webcsp

// Policy 是壳里页面的 CSP：
//   - 页面只能连自己（connect-src 'self'）：/api/ 全部经壳的会话代理（Wails 的绑定方法和流都在同源的 /wails/ 下），
//     图片和块经同源的取图地址，页面不能绕过壳直连服务器或存储（自检的 direct_blocked 一步验证）；
//   - 脚本只能是同源的文件（script-src 'self'，没有内联脚本、eval）：嵌入的界面、客户端自带的页面、Wails 的运行时
//     都在同源下；
//   - 样式允许内联（'unsafe-inline'）：Vue 的样式绑定、OpenSeadragon 和组件库都会写内联样式，脚本的限制才是关键；
//   - 图片：同源、data:（内联的图标）、blob:（OpenSeadragon 把取到的块转成 blob 再画）。
const Policy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; worker-src 'self' blob:; " +
	"object-src 'none'; base-uri 'self'; frame-src 'none'"

// DevPolicy 只给开发构建接 vite 开发服务器时用：另外放行 vite 热更新的 WebSocket（连本机的 vite）。
const DevPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self' ws://localhost:* ws://127.0.0.1:*; " +
	"worker-src 'self' blob:; object-src 'none'; base-uri 'self'; frame-src 'none'"
