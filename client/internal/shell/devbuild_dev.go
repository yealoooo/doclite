//go:build !production

package shell

// devBuild：开发构建（不带 production 标签，wails3 task build:dev、go run）。只有开发构建认 FRONTEND_DEVSERVER_URL
// （界面接 vite 的开发服务器热更新）。
const devBuild = true
