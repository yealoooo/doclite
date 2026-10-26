-- M11-6：存储用量（存储卡片的"已用 …· n 个快照"）用的索引（用户 2026-10-26 同意）。不改表结构。
--
-- 用量按 storage_profile_id 分组数 count(*)、sum(file_size)，存储迁移进行时存储页每 2 秒刷新一次。0001 只有
-- (storage_profile_id, storage_key)，不带 file_size，只能读整张表；带上 file_size 后只读这个索引（file_snapshots
-- 只追加，可见性图基本是全的）。
CREATE INDEX ix_file_snapshots_usage ON file_snapshots (storage_profile_id) INCLUDE (file_size);  -- 存储用量
