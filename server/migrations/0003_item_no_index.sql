-- M4-4：件号用的索引（用户同意，2026-10-15，M3 报告的 QA）。不改表结构。
--
-- 件号不回收（M3-2，用户决定 Q2）：新建条目取 max(item_no) + 1、手改件号查占用，都要算上已删除的条目。0001 的
-- ix_archive_items_category (category_id, item_no) 只含未删除的条目（WHERE deleted_at IS NULL），只靠它时这两条查询
-- 都是对 archive_items 的顺序扫描，而且在归类节点的行锁里执行（100 万个条目时约 45–94 ms 和 130 ms，M3-2 审查实测）。
-- 这个索引补上另一半：查询拆成"未删除的"（走 0001 的索引）和"已删除的"（走这个索引）两段，取两者的结果
-- （doclite_infra::archive_items 的 next_item_no、ensure_item_no_free）。两个部分索引合起来正好覆盖全部条目。
CREATE INDEX ix_archive_items_category_deleted ON archive_items (category_id, item_no)
  WHERE deleted_at IS NOT NULL;  -- 件号：已删除的条目（不回收）
