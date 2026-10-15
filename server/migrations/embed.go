// Package migrations 把建表 SQL 嵌进可执行文件。
//
// 文件名 `000N_说明.sql`，按文件名顺序执行，执行记录在 `schema_migrations` 表里
// （见 internal/platform/db 的 Migrate）。已经发布的文件不能再改：改了之后启动会报"校验和不一致"。
// 改表只新增文件。
package migrations

import "embed"

// FS 是全部迁移文件。
//
//go:embed *.sql
var FS embed.FS
