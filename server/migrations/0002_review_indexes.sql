-- M2-2：审核读接口用的索引（用户同意，2026-10-07）。不改表结构。
--
-- "我的审核"（GET /api/me/reviews）要取"每个文件最近一次分给我、没有取消的那一行"，只限当前项目。0001 里只有
-- ix_file_reviews_reviewer (reviewer_id, status)：按它取出的是这个人在所有项目里的全部历史，再回表按 project_id 过滤，
-- 耗时随审核人做过的项目越来越多而增长（实测 5 个已完成的项目、43 万行历史时约 200 ms）。这个索引让查询只碰
-- 这个人在这个项目里没取消的行（取消的行只是改派、删除留下的历史，读接口用不到）。
CREATE INDEX ix_file_reviews_reviewer_project ON file_reviews (reviewer_id, project_id)
  WHERE status <> 'cancelled';  -- 我的审核：某人在某项目里没取消的分配
