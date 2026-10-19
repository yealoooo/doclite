-- M10-1：按项目聚合的查询用的索引（server 性能优化，2026-10-19）。不改表结构。
--
-- 看板（GET /api/projects/{pid}/catalog-summary）整个项目时按 af.file_id = ff.file_id AND af.project_id = $1 把这个
-- 项目的挂接整个装进哈希连接。0001 的 archive_item_files 只有 (archive_item_id, file_id) 主键和 file_id 唯一索引，
-- 按 project_id 取只能顺序扫描整张表（所有项目的挂接）。这个索引让它只读这个项目的行，带上 archive_item_id 可以只扫
-- 索引（再连 archive_items 取状态）。
CREATE INDEX ix_archive_item_files_project ON archive_item_files (project_id, file_id) INCLUDE (archive_item_id);
  -- 看板：一个项目的挂接

-- 可以分配的人（GET /api/projects/{pid}/reviewers）、改派列表（GET /api/projects/{pid}/review-assignments 的
-- 按人计数和列表）按 r.project_id = $1 AND r.status = 'open' 取一个项目进行中的分配（可能再加 reviewer_id）。0001 的
-- ix_file_reviews_reviewer (reviewer_id, status) 和 0002 的 (reviewer_id, project_id) 都以审核人开头，按项目取只能
-- 顺序扫描整张 file_reviews（全部项目的全部历史，越用越大）。open 的行很少（每个文件最多一行），这个部分索引很小。
CREATE INDEX ix_file_reviews_project_open ON file_reviews (project_id, reviewer_id) INCLUDE (file_id)
  WHERE status = 'open';  -- 一个项目进行中的分配（按人）
