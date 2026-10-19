// Package upload 是壳的上传队列、热文件夹和重扫件上传（requirements 第 6 节"上传队列""热文件夹""每个文件"
// "重扫件上传"，5.6 节的请求格式、封面、查重、预检）。不依赖 Wails：给页面的命令和事件在 internal/shell 里接上。
//
//   - store.go     本机库 upload-queue.db（纯 Go 的 SQLite），一个数据目录只一个进程打开（旁边的 upload-queue.lock）；
//   - engine.go    调度：同一子文件夹按文件名自然顺序串行，最多 2 个子文件夹同时传；401、426 时整个队列暂停；
//   - process.go   每个文件：核对大小和修改时间 → 算 sha → 取或建文件夹 → 预检 → 生成封面 → 流式上传；
//   - hotfolder.go 热文件夹：fsnotify 只重新列出有事件的目录，加定时全量扫描兜底，写完判定（settle.go）后进队列；
//   - walk.go      列目录的规则（热文件夹和上传按钮选的文件夹共用）；
//   - rescan.go    重扫件：不进队列，一次一个；
//   - cover.go     封面：Go 解码 JPEG、PNG、TIFF，先按 2ⁿ 取平均缩小、转正（tiles.Shrink），再缩到长边 400 的 JPEG；
//   - api.go       发给 server 的请求（和页面共用会话：同一个 Cookie 罐和请求头，见 [Session]）。
//
// 纯逻辑（自然顺序、忽略规则、写完判定、重试间隔、multipart 字段顺序、封面尺寸）有单元测试；
// integration_test.go 连真 server 跑一遍上传（设置 DOCLITE_IT_SERVER 才跑）。
package upload
