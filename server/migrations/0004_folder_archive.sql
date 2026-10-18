-- M9-1：整个文件夹归档（用户 2026-10-18、19 同意改表，requirements 3.1、5.5、5.9）。
--
-- 条目只能由"整个文件夹归档"产生：条目记住来源文件夹（一个文件夹只对应一个未删除的条目），归档后来源文件夹封存；
-- 要改页走"解除封存（→ modifying）→ 在文件目录里改 → 重新归档（→ sealed）"。条目状态去掉 done，只有
-- draft（未锁定）/ locked（已锁定）。
--
-- 旧数据（用户 2026-10-19：旧数据不用管，开发库删掉重建）：为了在任何库上都能执行，旧的未删除条目在这里软删除、
-- 移出页（不变量：已删除的条目没有挂接行），文件本身、导出任务和明细都不动；已删除的条目没有来源文件夹（列可以为空，
-- 只要求未删除的条目有）。空库和测试模板库上这几条语句什么都不改。
UPDATE archive_items SET deleted_at = now(), row_version = row_version + 1, updated_at = now()
WHERE deleted_at IS NULL;
DELETE FROM archive_item_files;

UPDATE archive_items SET status = 'draft' WHERE status = 'done';
ALTER TABLE archive_items DROP CONSTRAINT archive_items_status_check;
ALTER TABLE archive_items ADD CONSTRAINT archive_items_status_check CHECK (status IN ('draft','locked'));  -- 未锁定 / 已锁定

ALTER TABLE archive_items
  ADD COLUMN source_folder_id uuid,                     -- 来源文件夹：条目由它整个归档而来，页 = 它里面的文件（硬性规则 4）
  ADD COLUMN source_status    text NOT NULL DEFAULT 'sealed'
    CHECK (source_status IN ('sealed','modifying')),    -- sealed 封存（文件夹不能改结构）/ modifying 修改中（解除封存，改完重新归档）
  ADD COLUMN unsealed_by      uuid REFERENCES users(id),  -- 最近一次解除封存的人（重新归档后清空，历史在审计里）
  ADD COLUMN unsealed_at      timestamptz,              -- 最近一次解除封存的时间
  ADD COLUMN unseal_reason    text,                     -- 解除封存的原因（选填，≤ 200 字）
  ADD CONSTRAINT archive_items_source_folder_fk
    FOREIGN KEY (source_folder_id, project_id) REFERENCES folders (id, project_id),  -- 来源文件夹必须在同一个项目
  ADD CONSTRAINT archive_items_source_required
    CHECK (deleted_at IS NOT NULL OR source_folder_id IS NOT NULL);  -- 未删除的条目一定有来源文件夹

CREATE UNIQUE INDEX ux_archive_items_source ON archive_items (source_folder_id)
  WHERE deleted_at IS NULL;  -- 一个文件夹只对应一个未删除的条目（封存检查、文件夹的归档信息也走它）
