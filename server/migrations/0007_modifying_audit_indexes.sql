-- M11-1：两个查询用的索引（server 按 review 优化，用户 2026-10-25 同意）。不改表结构。
--
-- 修改中的件（GET /api/projects/{pid}/archive-items:modifying）按 project_id = $1 AND deleted_at IS NULL AND
-- source_status = 'modifying' 取，按解除封存的时间倒序。0001 的 archive_items 没有以 project_id 开头的索引，只能顺序
-- 扫描整张表（所有项目的条目）。修改中的件很少，这个部分索引很小；列和查询的 ORDER BY 一致。
CREATE INDEX ix_archive_items_modifying ON archive_items (project_id, unsealed_at DESC NULLS LAST, id)
  WHERE source_status = 'modifying' AND deleted_at IS NULL;  -- 修改中的件

-- 审计日志（GET /api/audit-logs）按时间范围筛选。created_at 是写审计的事务开始的时间，和插入顺序基本一致，BRIN 索引
-- 很小，按时间范围只读相关的块。
CREATE INDEX ix_audit_logs_created ON audit_logs USING brin (created_at);  -- 审计：按时间筛选
