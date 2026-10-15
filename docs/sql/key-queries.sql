-- 参考写法，不是迁移脚本。$xxx 是占位符，实际代码里用 sqlx 的绑定参数；主键由应用生成 UUIDv7。
-- 表结构以 server/migrations/0001_init.sql 为准。
-- 加锁顺序见 design §2.1。项目文件树的咨询锁是 pg_advisory_xact_lock[_shared](6001, $project_lock_key)：
-- 6001 是文件树的锁类别，$project_lock_key 由项目 id 折叠成 int4（doclite_infra::tree::lock_key）；代码里用
-- doclite_infra::folders::lock_file_tree，不要手写。行锁用 FOR NO KEY UPDATE，例外见 design §2.1。

-- 上传一页（design §4.3）：multipart 里的原图流式写到临时文件并算出 sha256，只读文件头取格式、宽高、DPI、颜色模式；
-- 封面图由客户端生成、随请求一起上传（可以没有），server 只校验不生成，不解码图片；原图和封面图传到默认的 S3 存储
-- （key 按 项目编码/file_id/版本号 生成：v1.jpg、v1.cover.jpg）；最后开一个事务。
-- 实现见 doclite_infra::ingest（M1-7）：事务里依次 lock_file_tree(共享) → claim_file_slot（锁目标文件夹行，定名字和
-- sort_no）→ storage_profiles FOR KEY SHARE（仍是 active 且仍是默认，否则 409 storage_changed）→
-- pg_advisory_xact_lock(6003, (项目, sha) 折叠的键) 内容锁 → 锁内再查一次重（design §4.3、§2.1 第 6 步）→ 下面的插入。
BEGIN;
INSERT INTO files (id) VALUES ($file);
INSERT INTO file_snapshots (id, file_id, version_no, snapshot_type, storage_profile_id,
                            storage_key, cover_key,                               -- 原图、封面图（没带封面时为 NULL）
                            content_sha256, file_size, mime_type, upload_name,
                            width_px, height_px, dpi, color_mode, created_by)   -- 尺寸和 DPI 从文件头读出
VALUES ($snapshot, $file, 1, 'original', $default_profile,
        $key, $cover_key, $sha, $size, $mime, $name,
        $width, $height, $dpi, $color_mode, $user);
INSERT INTO folder_files (id, project_id, folder_id, file_id, file_name, sort_no, created_by)
VALUES ($ff, $pid, $folder, $file, $name, $seq, $user);          -- 审核状态默认“待分配”；重名时应用层自动改成 "IMG_0231 (1).jpg"
INSERT INTO file_logs (project_id, file_id, action, operator_id, detail)
VALUES ($pid, $file, 'upload', $user, jsonb_build_object('folder', $folder_name, 'folder_path', $folder_path,
                                                         'file_name', $name, 'upload_name', $upload_name));
COMMIT;

-- 上传前查重（design §4.3，预判和锁内复查都按这个顺序；当前版本 = max(version_no)，从 sha 的索引查起）：
-- 1. 断网重试：同一文件夹里未删除的文件，当前快照的 upload_name 和 sha 都相同 → 返回 200 和这个文件
SELECT ff.id, ff.file_id, ff.file_name FROM file_snapshots s JOIN folder_files ff ON ff.file_id = s.file_id
WHERE s.content_sha256 = $sha AND s.upload_name = $name AND ff.folder_id = $folder AND ff.deleted_at IS NULL
  AND s.version_no = (SELECT max(x.version_no) FROM file_snapshots x WHERE x.file_id = s.file_id)
LIMIT 1;
-- 2. 同项目里未删除的文件（所在文件夹也未删除），当前版本的 sha 相同 → 409 duplicate_content
--    （重扫时 $self 是这个文件自己，优先返回它；上传时 $self 为 NULL）
SELECT ff.file_id, ff.folder_id, ff.file_name FROM file_snapshots s
JOIN folder_files ff ON ff.file_id = s.file_id JOIN folders f ON f.id = ff.folder_id
WHERE s.content_sha256 = $sha AND ff.project_id = $pid AND ff.deleted_at IS NULL AND f.deleted_at IS NULL
  AND s.version_no = (SELECT max(x.version_no) FROM file_snapshots x WHERE x.file_id = s.file_id)
ORDER BY (ff.file_id = $self) IS TRUE DESC, f.path_code, ff.sort_no, ff.id
LIMIT 1;

-- 当前用户能进哪些项目：个人授权，或者拥有项目授权的某个角色（系统管理员另外处理：能进所有项目）
SELECT p.* FROM projects p
WHERE p.deleted_at IS NULL
  AND (EXISTS (SELECT 1 FROM project_members m WHERE m.project_id = p.id AND m.user_id = $user)
    OR EXISTS (SELECT 1 FROM project_roles pr JOIN user_roles ur ON ur.role_id = pr.role_id
               WHERE pr.project_id = p.id AND ur.user_id = $user));

-- 当前用户的菜单（多个角色取并集）
SELECT DISTINCT m.menu_code, m.parent_code, m.menu_name, m.route_path, m.sort_no
FROM user_roles ur
JOIN role_menus rm ON rm.role_id = ur.role_id
JOIN menus m       ON m.menu_code = rm.menu_code
WHERE ur.user_id = $user ORDER BY m.sort_no;

-- 打开一个文件夹（GET /api/folders/{id}/children）：子文件夹和文件一条 SQL 取完，都按 sort_no、名称排序，不分页。
-- 列表只返回 file_id 和业务字段：不查 cover_key、storage_key，也不预签名（design §4.4）；
-- 图片信息由客户端按 file_id 调 POST /api/files/batch 取。实现见 doclite_infra::folders。
SELECT 0 AS kind, c.id, c.folder_name AS name, c.remark, c.path_code, c.sort_no,
       (SELECT count(*) FROM folders g WHERE g.parent_id = c.id AND g.deleted_at IS NULL) AS folder_count,
       (SELECT count(*) FROM folder_files x WHERE x.folder_id = c.id AND x.deleted_at IS NULL) AS file_count,
       NULL::uuid AS file_id, NULL::text AS review_status, NULL::int AS review_round,
       NULL::int AS version_no, NULL::bool AS cataloged, c.created_at
FROM folders c WHERE c.parent_id = $folder AND c.deleted_at IS NULL
UNION ALL
SELECT 1, ff.id, ff.file_name, NULL, NULL, ff.sort_no, NULL, NULL,
       ff.file_id, ff.review_status, ff.review_round,
       (SELECT max(s.version_no) FROM file_snapshots s WHERE s.file_id = ff.file_id),     -- 当前版本号
       EXISTS (SELECT 1 FROM archive_item_files af WHERE af.file_id = ff.file_id),        -- 是否已归类
       ff.created_at
FROM folder_files ff WHERE ff.folder_id = $folder AND ff.deleted_at IS NULL
ORDER BY kind, sort_no, name, id;

-- 下面两段是原理。实现（doclite_infra::tree）的差别：编码在应用里拼（序号超过 99999 返回 409 tree_full，不用 lpad）；
-- 事务第一步先拿项目文件树的咨询锁（新建拿共享锁、移动拿排他锁），再按 id 升序锁住要用的文件夹行（design §2.1）。
-- 子树用区间 path_code >= $lo AND path_code < $hi，不用带绑定参数的 LIKE：$lo 是前缀本身，$hi 是把前缀末尾的点换成 /
-- （doclite_domain::tree::subtree_range）。

-- 新建子文件夹：在父文件夹上原子地领一个序号，拼出自己的路径编码（例如 00001.00001.00003.）
WITH n AS (UPDATE folders SET last_child_no = last_child_no + 1 WHERE id = $parent
           RETURNING project_id, path_code || lpad(last_child_no::text, 5, '0') || '.' AS path_code)
INSERT INTO folders (id, project_id, parent_id, folder_name, path_code, created_by)
SELECT $id, n.project_id, $parent, $name, n.path_code, $user FROM n
RETURNING path_code;

-- 移动文件夹：先防环，再在新父文件夹下领一个编码，用一条 UPDATE 给整棵子树换前缀
SELECT starts_with($target_path, $folder_path) AS would_cycle;       -- true 就拒绝：目标在自己的子树里（tree::is_in_subtree）
WITH n AS (UPDATE folders SET last_child_no = last_child_no + 1 WHERE id = $target
           RETURNING path_code || lpad(last_child_no::text, 5, '0') || '.' AS new_prefix)
UPDATE folders f SET path_code = n.new_prefix || substr(f.path_code, length($folder_path) + 1), updated_at = now()
FROM n WHERE f.project_id = $pid AND f.path_code >= $lo AND f.path_code < $hi;   -- $lo = $folder_path，$hi 见上
UPDATE folders SET parent_id = $target WHERE id = $folder;

-- 归类树（M3-1，实现见 doclite_infra::catalog 和通用树 doclite_infra::tree，规则见 design §2.1 的"归类的加锁顺序"、
-- §2.3 的"归类节点"、§2.5）。6002 是归类树的锁类别（doclite_infra::catalog::lock_category_tree，同时确认项目未删除；它不挡项目删除，见 design §2.1）。
-- 归类树本身的写操作不拿 6001。同时要拿两棵树的锁的事务（项目改名，M3-3 的挂接、移出、页序）先拿 6002 共享锁、
-- 再锁归类的行（节点、条目），最后才拿 6001 共享锁（design §2.1：先 6002 后 6001 是 M3-1 验收时总控改的，归类的行
-- 在 6001 之前是 M3-3 审查时改的）：等 6002、等归类的行的事务手里都没有 6001，不会让文件树上排队的审核、上传跟着等。
--
-- 修改项目带了新名称时（PATCH /api/system/projects/{id}，实现见 doclite_infra::projects）：
-- BEGIN;
-- SELECT pg_advisory_xact_lock_shared(6002, $project_lock_key);     -- 1. 归类树的共享锁（改名要改归类根）
-- SELECT id FROM archive_categories WHERE project_id = $pid AND parent_id IS NULL
-- FOR NO KEY UPDATE;                                                 --    归类根的行（M3-3 审查：归类的行在 6001 之前）
-- SELECT pg_advisory_xact_lock_shared(6001, $project_lock_key);     -- 2. 文件树的共享锁（改名要改根文件夹）
-- SELECT … FROM projects WHERE id = $pid FOR NO KEY UPDATE;          -- 项目行；带了 expected_updated_at 时和这里读到的
--                                                                   --   updated_at 比较，不相等 → 409 conflict（M4-4）
-- UPDATE projects SET …, updated_at = GREATEST(now(), updated_at + interval '1 microsecond') WHERE id = $pid;
--                                                                   -- 版本严格递增（改授权也这样写，M4-4）
-- UPDATE folders SET folder_name = $name WHERE project_id = $pid AND parent_id IS NULL;
-- UPDATE archive_categories SET category_name = $name WHERE project_id = $pid AND parent_id IS NULL;
-- INSERT INTO audit_logs …; COMMIT;
-- 只改说明、状态、settings 时不拿 6002、不锁归类根（从第 2 步开始）。

-- 新建归类节点（POST /api/projects/{pid}/archive-categories）：事务外先校验类型（白名单，不能是 root）、名称和编码
-- （编码按当前档号模板的分隔符，doclite_domain::catalog::CodeRule）。$parent 不传时是根节点。
BEGIN;
SELECT pg_advisory_xact_lock_shared(6002, $project_lock_key);        -- 1. 归类树的共享锁（不拿 6001）
SELECT EXISTS (SELECT 1 FROM projects WHERE id = $pid AND deleted_at IS NULL);   -- 没有 → 404
SELECT id FROM archive_categories WHERE project_id = $pid AND parent_id IS NULL;  -- 不传 parent_id 时：根节点
SELECT id, path_code, deleted_at FROM archive_categories             -- 2. 锁父节点行：同一父节点下的新建、改编码串行
WHERE id = $parent AND project_id = $pid FOR NO KEY UPDATE;          -- 没有或已删除 → 404
SELECT id FROM archive_categories                                    -- 同级按编码判重，区分大小写（和
WHERE parent_id = $parent AND deleted_at IS NULL AND category_code = $code LIMIT 1;  -- ux_archive_categories_code 一致）→ 409 conflict
UPDATE archive_categories SET last_child_no = last_child_no + 1 WHERE id = $parent
RETURNING path_code, last_child_no;                                  -- 应用里拼编码（超过 99999 → 409 tree_full）
INSERT INTO archive_categories (id, project_id, parent_id, path_code, sort_no, category_type, category_code, category_name)
VALUES ($id, $pid, $parent, $path, $child_no, $type, $code, $name);  -- sort_no = 父节点下的序号（排在末尾）
INSERT INTO audit_logs (project_id, operator_id, target_type, target_id, action, detail)
VALUES ($pid, $user, 'archive_category', $id, 'create', $after);     -- {after: {category_name, category_code, category_type,
COMMIT;                                                              --   path_code, parent_id, category_path}}；提交后发 catalog.changed

-- 移动归类节点并重算档号（PATCH /api/projects/{pid}/archive-categories/{id} 带 parent_id；改 category_code、
-- category_type 一样走排他锁、定稿检查和重算，只是不换编码）。$lo/$hi 是节点子树的编码区间（subtree_range）。
BEGIN;
SELECT pg_advisory_xact_lock(6002, $project_lock_key);               -- 1. 归类树的排他锁：子树不会再变，也没人能建条目
SELECT id, parent_id, path_code, category_code, deleted_at FROM archive_categories   -- 2. 按 id 升序锁住自己、原父节点、
WHERE id = ANY(ARRAY[$id, $old_parent, $target]) AND project_id = $pid             --    新父节点；不存在、已删除 → 404；
ORDER BY id FOR NO KEY UPDATE;                                       --    根节点 → 409 invalid_state
SELECT starts_with($target_path, $node_path) AS would_cycle;         -- true → 409 tree_cycle
SELECT id FROM archive_categories                                    -- 新父节点下同级编码重复 → 409 conflict
WHERE parent_id = $target AND deleted_at IS NULL AND category_code = $code AND id <> $id LIMIT 1;
SELECT a.id, a.status, a.archive_no, a.title FROM archive_items a    -- 3. 按 id 升序锁住子树里未删除的条目：
JOIN archive_categories c ON c.id = a.category_id                    --    有 status = 'locked' 的 → 409 item_locked，
WHERE c.project_id = $pid AND c.path_code >= $lo AND c.path_code < $hi   --    什么都不改
  AND a.deleted_at IS NULL
ORDER BY a.id FOR NO KEY UPDATE OF a;
WITH n AS (UPDATE archive_categories SET last_child_no = last_child_no + 1 WHERE id = $target
           RETURNING path_code || lpad(last_child_no::text, 5, '0') || '.' AS new_prefix)
UPDATE archive_categories c SET path_code = n.new_prefix || substr(c.path_code, length($node_path) + 1)
FROM n WHERE c.project_id = $pid AND c.path_code >= $lo AND c.path_code < $hi;    -- 整棵子树（含已删除的）换前缀
UPDATE archive_categories SET category_name = $name, category_code = $code, parent_id = $target, sort_no = $child_no
WHERE id = $id;                                                      -- 名称、编码、parent_id 一条语句改（唯一索引按语句检查）
-- 重算档号（doclite_infra::catalog::renumber_subtree）：$lo2/$hi2 是新编码的区间。读子树里未删除、未定稿的条目和
-- 子树、子树根的祖先（都未删除）的类型和编码，在应用里按模板生成（doclite_domain::archive_no：每个占位符取祖先链上
-- 最近的同类型节点的编码，缺级为 NULL）。
SELECT a.id, a.category_id, a.item_no, a.archive_no, c.path_code FROM archive_items a
JOIN archive_categories c ON c.id = a.category_id
WHERE c.project_id = $pid AND c.path_code >= $lo2 AND c.path_code < $hi2
  AND c.deleted_at IS NULL AND a.deleted_at IS NULL AND a.status <> 'locked'
ORDER BY a.id;                                                       -- = $candidates，按 id 升序（分配的先后）
SELECT path_code, category_type, category_code FROM archive_categories
WHERE project_id = $pid AND deleted_at IS NULL
  AND ((path_code >= $lo2 AND path_code < $hi2) OR path_code = ANY($ancestor_paths));
SELECT archive_no, id FROM archive_items                             -- 不参与重算的条目（范围外的、定稿的）先占着现在的
WHERE project_id = $pid AND deleted_at IS NULL                       -- 档号。$probe = 候选生成的档号去掉候选自己现在持有
  AND archive_no = ANY($probe);                                      -- 的号（在应用里去；唯一索引保证那些号不会被别人占），
                                                                     -- 所以查到的占用者一定不是候选，不用带候选 id 的数组
-- M3-2 复审：原来写 archive_no = ANY($wanted) AND NOT (id = ANY($candidates))，档号没变的候选全部命中，同一个连接执行
-- 5 次以后 PG 用通用计划，NOT (id = ANY(…)) 对每个命中的行线性比较，O(N²)（3 万个条目约 7 秒，拿着 6002 排他锁）。
-- 撞号（M3-2 审查、复审，总控决定）：在应用里分档先到先得，档内按 id 升序——① 现在有档号、不用换号的，② 现在有档号、
-- 要换号的，③ 现在档号为空的；生成的档号已经被占的留空（NULL），不报错，结果确定、重复执行不变（原来持号的不会被
-- 撞号留空、id 更小的条目抢走）。档号变了的（含变成 NULL 的）是 $ids / $new_nos，撞号的审计 detail 另带
-- conflict {archive_no, item_id}。
-- ux_archive_items_no 不能延迟检查（逐行检查）：一条 UPDATE 里互换两个档号会误报冲突，所以先把"旧档号正好是
-- 别的条目的新档号"的那些（$reused，在应用里算；互换、轮换时才有，移动、改编码时通常没有）置空，再写。
-- 改档号不是 HOT 更新（它有唯一索引），每一行都要往全部索引（含题名的 GIN）里写，能少一遍 UPDATE 就少一遍。
UPDATE archive_items SET archive_no = NULL WHERE id = ANY($reused);  -- $reused 为空时不执行
UPDATE archive_items a SET archive_no = t.archive_no, row_version = a.row_version + 1, updated_by = $user, updated_at = now()
FROM unnest($ids::uuid[], $new_nos::text[]) AS t(id, archive_no) WHERE a.id = t.id;
INSERT INTO audit_logs (project_id, operator_id, target_type, target_id, action, detail)      -- 每个条目一条
SELECT $pid, $user, 'archive_item', t.id, 'renumber', t.detail     -- {before: {archive_no}, after: {archive_no},
FROM unnest($ids::uuid[], $details::jsonb[]) AS t(id, detail);       --  cause: {category_id, changes: ["move"]}}
INSERT INTO audit_logs (project_id, operator_id, target_type, target_id, action, detail)
VALUES ($pid, $user, 'archive_category', $id, 'move', $before_after);   -- after 里带 renumbered（有撞号时另带
                                                                     --   archive_no_conflicts）
COMMIT;                                                              -- 提交后发 catalog.changed {category_ids: [自己, 原父, 新父],
                                                                     --   item_ids: $ids（超过 500 个不带）}

-- 删除归类节点（DELETE /api/projects/{pid}/archive-categories/{id}）：6002 排他锁 → 子树里有未删除的条目
-- （SELECT count(*) … archive_items JOIN archive_categories … path_code 区间 AND a.deleted_at IS NULL）→ 409
-- category_not_empty；否则按 id 升序锁住子树里未删除的节点行，打上 deleted_at（编码不回收），写审计 delete。

-- 归类树按需加载（GET /api/projects/{pid}/archive-categories?parent_id=，M3-1；实现见 doclite_infra::catalog 的
-- children）：父节点（不传是根节点）、它的直接子节点和计数，三条在同一个只读快照里，不加锁。$path/$lo/$hi 是父节点的
-- 编码和子树区间。
SELECT id, path_code FROM archive_categories                         -- 父节点（不传 parent_id 时按 parent_id IS NULL 取根）；
WHERE id = $parent AND project_id = $pid AND deleted_at IS NULL;     --   不在这个项目里、已删除 → 404
SELECT id, project_id, parent_id, path_code, category_type, category_code, category_name, sort_no, created_at,
       (SELECT count(*) FROM archive_categories g                    -- child_count：未删除的直接子节点数
        WHERE g.project_id = c.project_id AND g.parent_id = c.id AND g.deleted_at IS NULL) AS child_count
FROM archive_categories c
WHERE c.project_id = $pid AND c.parent_id = $parent AND c.deleted_at IS NULL
ORDER BY c.sort_no, c.category_code, c.id;
SELECT left(c.path_code, length($path) + 6) AS head, count(*)        -- item_count：子树里未删除的条目按"落在哪个直接子节点
FROM archive_categories c JOIN archive_items a ON a.category_id = c.id   -- 的子树里"分组（head 等于 $path 的是直接放在
WHERE c.project_id = $pid AND c.path_code >= $lo AND c.path_code < $hi   -- 父节点里的）；父节点的 item_count 是全部之和
  AND c.deleted_at IS NULL AND a.deleted_at IS NULL
GROUP BY 1;
-- 归类设置（GET /api/projects/{pid}/catalog-settings，能进项目即可）只读这一列，默认值在应用里逐层合并
-- （doclite_domain::catalog::CatalogSettings::from_stored，design §2.5）；模板解析不了时返回原因，不报错。
SELECT settings FROM projects WHERE id = $pid AND deleted_at IS NULL;   -- 没有 → 404

-- 建条目（POST /api/archive-categories/{id}/items，M3-2；实现见 doclite_infra::archive_items，规则见 design §2.3 的
-- "档案条目"）。路由上先经归属助手（节点所在的项目、能不能进）。不拿 6001：不动文件树（M3-3 带 file_ids 时才拿）。
BEGIN;
SELECT pg_advisory_xact_lock_shared(6002, $project_lock_key);        -- 1. 归类树的共享锁（同时确认项目未删除）
SELECT path_code FROM archive_categories                             -- 2. 锁所在节点行：同一个节点里分配件号的事务串行
WHERE id = $category AND project_id = $pid AND deleted_at IS NULL    --    （移动条目锁目标节点、手改件号锁所在节点，
FOR NO KEY UPDATE;                                                   --    也是这一行）；没有 → 404
SELECT settings FROM projects WHERE id = $pid;                       -- 模板、著录项（补上默认值，CatalogSettings）
-- 应用里：没提到的著录项填 default，按草稿规则校验类型和格式（不合规则 400 invalid_input，details.fields）。
SELECT GREATEST(                                                     -- 件号：算上已删除的条目（不回收）。拆成两段（M4-4）：
  (SELECT max(item_no) FROM archive_items                            --   未删除的走 0001 的 ix_archive_items_category，
   WHERE category_id = $category AND deleted_at IS NULL),            --   已删除的走 0003 的 ix_archive_items_category_deleted，
  (SELECT max(item_no) FROM archive_items                            --   各是一次索引倒序取第一行；只写 category_id = $category
   WHERE category_id = $category AND deleted_at IS NOT NULL),        --   时哪个部分索引都用不上，是顺序扫描（M3-2 的写法）
  0) + 1;                                                            --   doclite_infra::archive_items::MAX_ITEM_NO_SQL
SELECT category_type, category_code FROM archive_categories          -- 祖先链（含自己，未删除的，按编码精确查）
WHERE project_id = $pid AND path_code = ANY($ancestor_paths_and_self) AND deleted_at IS NULL
ORDER BY path_code;                                                  -- 应用里按模板生成档号，缺级为 NULL
SELECT id FROM archive_items                                         -- 撞号：生成的档号已经被别的未删除条目占着 → 档号
WHERE project_id = $pid AND deleted_at IS NULL AND archive_no = $archive_no LIMIT 1;   -- 留空（NULL），不报错（M3-2 审查）
INSERT INTO archive_items (id, project_id, category_id, item_level, item_no, archive_no, title, extra_fields, status, created_by)
VALUES ($id, $pid, $category, 'item', $item_no, $archive_no, $title, $extra_fields, 'draft', $user)
ON CONFLICT (project_id, archive_no) WHERE archive_no IS NOT NULL AND deleted_at IS NULL
DO NOTHING;                                                          -- 另一个事务同时生成了同一个档号：等它提交后什么都
                                                                     -- 不做（影响 0 行），再以 archive_no = NULL 插一次
INSERT INTO audit_logs (project_id, operator_id, target_type, target_id, action, detail)
VALUES ($pid, $user, 'archive_item', $id, 'create', $after);        -- {after: {category_id, category_path, item_no,
COMMIT;                                                              --   archive_no, title, extra_fields, status}，撞号留空时
                                                                     --   另有 archive_no_conflict {archive_no, item_id}}；提交后发
                                                                     --   catalog.changed {category_ids: [节点], item_ids: [条目]}
-- 修改（PATCH /api/archive-items/{id}）同样的顺序：6002 共享锁 → 不加锁地读条目在哪个节点 →（移动时）锁目标节点行、
-- （改件号时）锁所在节点行 → SELECT … FROM archive_items WHERE id = $id FOR NO KEY UPDATE → 定稿的 409 item_locked →
-- row_version 不等 409 conflict → 合并、校验著录（旧值不改就不校验）→ 件号（移动取目标节点 max+1；手改的要在节点里没被
-- 占用，含已删除的，否则 409 conflict）→ 重算档号：带了 item_no 时撞号 409 conflict；只移动时撞号留空，UPDATE 在保存点里
-- 做（SAVEPOINT …; UPDATE …; 撞上 ux_archive_items_no 就 ROLLBACK TO SAVEPOINT，以 archive_no = NULL 再 UPDATE）→
-- UPDATE … row_version = row_version + 1 → 审计 update / move（撞号留空时 after 带 archive_no_conflict）。
-- 对应的 SQL（doclite_infra::archive_items 的 update；$lock_category 是目标节点或所在节点）：
BEGIN;
SELECT pg_advisory_xact_lock_shared(6002, $project_lock_key);        -- 1. 归类树的共享锁（同时确认项目未删除）
SELECT category_id FROM archive_items                                -- 不加锁地读条目现在在哪个节点（决定第 2 步锁哪一行）；
WHERE id = $id AND project_id = $pid AND deleted_at IS NULL;         --   没有 → 404
SELECT path_code FROM archive_categories                             -- 2. 移动时锁目标节点，只改件号时锁所在节点（只改著录
WHERE id = $lock_category AND project_id = $pid AND deleted_at IS NULL    --    不锁节点）；目标节点没有或已删除 → 404
FOR NO KEY UPDATE;
SELECT status, row_version, category_id, item_no, archive_no, title, extra_fields
FROM archive_items WHERE id = $id AND project_id = $pid AND deleted_at IS NULL
FOR NO KEY UPDATE;                                                   -- 3. locked → 409 item_locked；row_version 不等、或者
                                                                     --    category_id 和上面读的不同 → 409 conflict
SELECT settings FROM projects WHERE id = $pid AND deleted_at IS NULL;    -- 应用里合并、校验著录（400 details.fields）；
                                                                     -- done 的条目著录变了时再查必填
-- 移动、没带 item_no：目标节点的 max+1（算上已删除的），同"建条目"的两段写法（$category 换成 $target）。
SELECT title, deleted FROM (                                         -- 带了 item_no（换了节点或和现在的不同）：节点里已有
  (SELECT title, false AS deleted FROM archive_items                 -- 这个件号（含已删除的）→ 409 conflict。两段（M4-4），
   WHERE category_id = $new_category AND item_no = $item_no          -- 各走一个部分索引；两段都有时报未删除的（提示里写
     AND id <> $id AND deleted_at IS NULL LIMIT 1)                   -- 题名），只有已删除的占着时提示"件号不回收"
  UNION ALL                                                          -- （doclite_infra::archive_items::ITEM_NO_TAKEN_SQL）
  (SELECT title, true FROM archive_items
   WHERE category_id = $new_category AND item_no = $item_no
     AND id <> $id AND deleted_at IS NOT NULL LIMIT 1)
) t ORDER BY deleted LIMIT 1;
-- 换了节点或件号时按新节点的祖先链生成档号（同"建条目"），查占用者（同"档号为空的原因"）：带了 item_no → 409 conflict；
-- 只移动 → 留空。著录、节点、件号都没变 → 200，不写库、不写审计、不发事件。
SAVEPOINT patch_item;                                                -- 只有"只移动、新档号不为空"时才用保存点
UPDATE archive_items SET title = $title, extra_fields = $extra_fields, category_id = $new_category, item_no = $item_no,
       archive_no = $archive_no, row_version = row_version + 1, updated_by = $user, updated_at = now()
WHERE id = $id;                                                      -- 撞上 ux_archive_items_no → ROLLBACK TO SAVEPOINT
RELEASE SAVEPOINT patch_item;                                        --   patch_item，以 archive_no = NULL 再 UPDATE 一次
INSERT INTO audit_logs (project_id, operator_id, target_type, target_id, action, detail)
VALUES ($pid, $user, 'archive_item', $id, $action, $detail);         -- 换了节点 'move'，否则 'update'；{before, after} 只放
COMMIT;                                                              --   变了的字段；提交后发 catalog.changed {category_ids:
                                                                     --   [原节点, 新节点], item_ids: [条目]}
--
-- 详情的"档号为空的原因"（archive_no_reason，读的时候算，不存）：按现在的模板和祖先链渲染，缺级 → incomplete；渲染得出
-- $no 时查占用者 → 有就是 conflict（给出它的 id、节点、件号），没有就是 not_generated：
SELECT id, category_id, item_no, title FROM archive_items
WHERE project_id = $pid AND deleted_at IS NULL AND archive_no = $no AND id <> $id LIMIT 1;

-- 标记完成、退回草稿（POST /api/archive-items/{id}/mark-done、/back-to-draft {row_version}，M3-2；实现见
-- doclite_infra::archive_items 的 transition，和下面的定稿、解锁是同一个函数）。不锁节点行，不拿 6001。
BEGIN;
SELECT pg_advisory_xact_lock_shared(6002, $project_lock_key);        -- 1. 归类树的共享锁（同时确认项目未删除）
SELECT status, row_version, category_id, title, extra_fields         -- 3. 锁条目行；没有 → 404
FROM archive_items WHERE id = $id AND project_id = $pid AND deleted_at IS NULL
FOR NO KEY UPDATE;
-- 依次：locked → 409 item_locked（要先解锁）；标记完成要 draft、退回草稿要 done，否则 409 invalid_state；row_version
-- 不等 → 409 conflict（message 写明现在是第几版）；标记完成时题名和必填不能为空、现有的值要符合现在的设置
-- （completion_errors，否则 400 invalid_input + details.fields）。退回草稿不查著录。
SELECT settings FROM projects WHERE id = $pid AND deleted_at IS NULL;    -- 著录项（补上默认值）
UPDATE archive_items SET status = $next, row_version = row_version + 1, updated_by = $user, updated_at = now()
WHERE id = $id;                                                      -- 'done' / 'draft'
INSERT INTO audit_logs (project_id, operator_id, target_type, target_id, action, detail)
VALUES ($pid, $user, 'archive_item', $id, $action, $detail);         -- 'mark_done' / 'back_to_draft'：
COMMIT;                                                              --   {before: {status}, after: {status}}；响应是同一个事务里
                                                                     --   重读的详情；提交后发 catalog.changed {category_ids:
                                                                     --   [所在节点], item_ids: [条目]}

-- 定稿、解锁（POST /api/archive-items/{id}/lock、/unlock，M3-2）。解锁的菜单检查（catalog 和 review_assign 都要）在路由上。
BEGIN;
SELECT pg_advisory_xact_lock_shared(6002, $project_lock_key);        -- 1. 归类树的共享锁（M3-3 的挂接也拿它，再锁条目行）
SELECT status, row_version, archive_no, title, extra_fields          -- 3. 锁条目行：挂接、页序（M3-3）也先锁它，页数在这一刻
FROM archive_items WHERE id = $id AND project_id = $pid AND deleted_at IS NULL   --    之后不会变；没有 → 404
FOR NO KEY UPDATE;
-- 状态：定稿要 done（draft、locked → 409 invalid_state）；解锁要 locked。row_version 不等 → 409 conflict。
-- 定稿时：著录按"标记完成"的规则查（必填不为空、现有的值符合现在的设置，否则 400 invalid_input + details.fields）。
SELECT count(*),                                                     -- 页和审核状态（按这一刻读，不锁 files 行：审核不拿
       count(*) FILTER (WHERE ff.review_status <> 'approved'),       -- 归类的锁，定稿之后审核状态照样会变）
       min(af.page_no) FILTER (WHERE ff.review_status <> 'approved')
FROM archive_item_files af JOIN folder_files ff ON ff.file_id = af.file_id
WHERE af.archive_item_id = $id;
-- 没有页、archive_no 为空（message 写明缺级还是撞号，同详情的 archive_no_reason）、（catalog.lock_requires_approved 为 true
-- 时）有页没通过 → 409 invalid_state，什么都不改。
UPDATE archive_items SET status = 'locked', row_version = row_version + 1, updated_by = $user, updated_at = now()
WHERE id = $id;                                                      -- 解锁：status = 'done'
INSERT INTO audit_logs (project_id, operator_id, target_type, target_id, action, detail)
VALUES ($pid, $user, 'archive_item', $id, 'lock', $detail);         -- {before: {status}, after: {status}, archive_no, page_count,
COMMIT;                                                              --   lock_requires_approved}；解锁 'unlock'；提交后发 catalog.changed
-- 批量定稿（POST /api/projects/{pid}/archive-items:lock {item_ids}）：同样先 6002 共享锁，再
-- SELECT … FROM archive_items WHERE id = ANY($ids) AND project_id = $pid AND deleted_at IS NULL ORDER BY id FOR NO KEY UPDATE；
-- 缺的 → 404 details.ids；已定稿的跳过；其余任何一个不满足 → 409 invalid_state details.ids，整体不做；否则一条 UPDATE …
-- WHERE id = ANY($locked)，审计用一条 INSERT … SELECT FROM unnest($ids, $details)（detail 带 batch: true）。
--
-- "定稿后有新版本"（列表、详情，不改表）：最近一次定稿的时间是最近一条 lock 审计（ix_audit_logs_target），
-- 有页的快照比它晚就标出来：
SELECT x.id, l.created_at AS locked_at,
       EXISTS (SELECT 1 FROM archive_item_files af JOIN file_snapshots s ON s.file_id = af.file_id
               WHERE af.archive_item_id = x.id AND s.created_at > l.created_at) AS new_version_after_lock
FROM unnest($locked_item_ids::uuid[]) AS x(id)
CROSS JOIN LATERAL (SELECT created_at FROM audit_logs
                    WHERE target_type = 'archive_item' AND target_id = x.id AND action = 'lock'
                    ORDER BY id DESC LIMIT 1) l;
--
-- 按新模板重新生成档号（POST /api/projects/{pid}/archive-items:renumber {category_id?}）：6002 排他锁 → 节点（不传是根）
-- → renumber_subtree（见上面"移动归类节点并重算档号"的后半段，cause {category_id, changes: ["template"]}，跳过定稿的；
-- 撞号的分档先到先得、后到的留空，数出 archive_no_conflicts；模板不变时重复执行不改任何条目）→ 数一下范围里定稿的条目
-- （skipped_locked）→ 有变化时
-- 给范围的根写一条 archive_category / renumber 审计（after {template, renumbered, skipped_locked, archive_no_conflicts}）。

-- 条目列表（GET /api/archive-categories/{id}/items?limit=&cursor=，M3-2；实现见 doclite_infra::archive_items 的 list）：
-- 直接放在这个节点里的未删除条目（不含下级节点），几条在同一个只读快照里，不加锁。
SELECT EXISTS (SELECT 1 FROM archive_categories                      -- 节点不在这个项目里、已删除 → 404
               WHERE id = $category AND project_id = $pid AND deleted_at IS NULL);
SELECT status, count(*) FROM archive_items                           -- counts {draft, done, locked}，total 是三者之和；
WHERE category_id = $category AND deleted_at IS NULL GROUP BY status;   -- 不受分页影响
SELECT id, category_id, item_no, archive_no, title, status, row_version, updated_at
FROM archive_items
WHERE category_id = $category AND deleted_at IS NULL
  AND item_no >= $c_no AND (item_no > $c_no OR id > $c_id)           -- keyset：cursor 是 "<item_no>_<id>"（第一页没有这一行，
ORDER BY item_no, id LIMIT $limit + 1;                               -- 代码里是另一条语句）；多取一行判断 next_cursor
SELECT af.archive_item_id, ff.review_status, count(*)                -- 这一页各条目每种审核状态的页数（page_counts，
FROM archive_item_files af JOIN folder_files ff ON ff.file_id = af.file_id    -- page_count 是它们的和）
WHERE af.archive_item_id = ANY($page_item_ids)
GROUP BY 1, 2;
-- 这一页里 locked 的条目再查 locked_at、new_version_after_lock（上面"定稿后有新版本"那一条）；不是 locked 的为 null、false。

-- 条目详情（GET /api/archive-items/{id}；新建、修改、状态动作、PUT 页序的响应也是它，在写事务里重读）：实现见
-- doclite_infra::archive_items 的 build_detail。GET 在一个只读快照里读，不加锁。
SELECT settings FROM projects WHERE id = $pid AND deleted_at IS NULL;    -- 著录项（field_issues 在应用里按它算）、模板
SELECT id, project_id, category_id, item_no, archive_no, title, extra_fields, status, row_version,
       created_by, created_at, updated_by, updated_at
FROM archive_items WHERE id = $id AND project_id = $pid AND deleted_at IS NULL;   -- 没有 → 404
SELECT category_name FROM archive_categories                         -- category_path：所在节点的祖先和自己，从根节点（项目名）
WHERE project_id = $pid AND path_code = ANY($ancestor_paths_and_self)    --   起用 / 连接（按编码精确查）
ORDER BY path_code;
SELECT created_at FROM audit_logs                                    -- locked_at：只有 locked 的条目查，最近一条 lock 审计
WHERE target_type = 'archive_item' AND target_id = $id AND action = 'lock'   --   （解锁后不标）
ORDER BY id DESC LIMIT 1;
SELECT af.file_id, af.page_no, ff.file_name, ff.folder_id, ff.review_status, ff.review_round,
       v.version_no, v.created_at AS version_at                      -- 当前版本 = version_no 最大的快照；应用里 version_at
FROM archive_item_files af                                           --   > locked_at 的页标 new_version_after_lock，条目的
JOIN folder_files ff ON ff.file_id = af.file_id                      --   new_version_after_lock 是任何一页为 true；
CROSS JOIN LATERAL (SELECT s.version_no, s.created_at FROM file_snapshots s       --   page_counts 按 review_status 数
                    WHERE s.file_id = af.file_id ORDER BY s.version_no DESC LIMIT 1) v
WHERE af.archive_item_id = $id
ORDER BY af.page_no;
-- 每页的 folder_path：对这些页的 folder_id 一次查出（doclite_infra::files::folder_names，同 M2-2 的"文件夹路径"）。
-- archive_no 为 NULL 时另算 archive_no_reason（上面"档号为空的原因"）。

-- 挂接、重排、移出（M3-3；实现见 doclite_infra::archive_items 的 replace_pages，规则见 design §2.3 的"档案条目"、§7）。
-- PUT /api/archive-items/{id}/files {row_version, file_ids}：file_ids 是条目的全部页（按页序，0–2000 个，不能重复，否则
-- 事务外就 400）。新挂上的记 link、移出的记 unlink、留下的页相对顺序变了记一条 reorder 审计，页号重排成 1..n。
-- 带 file_ids 新建条目（POST /api/archive-categories/{id}/items）在上面"建条目"的插入之后做同样的事（现在的页为空）；
-- 删除条目（DELETE /api/archive-items/{id}）是"换成空列表"再打删除标记（不变量：已删除的条目没有挂接行）。
-- 加锁顺序（design §2.1 的归类顺序，M3-3 审查改的）：6002 共享 → 条目行（新建时是归类节点行，再插入条目）→ 有新挂上
-- 或移出的页时 6001 共享 → files 行（id 升序）。先锁归类的行再拿 6001：原来 6001 在条目行之前，条目行被批量定稿、
-- PATCH 这类只拿 6002 的操作占着时，PUT 拿着 6001 在等，后面的移动、删除文件夹（6001 排他）和再后面的审核都跟着等。
-- 审核的写操作不拿 6002、不锁条目，挂接和审核只会在同一个文件的 files 行上短暂排队（不成环：两边都按 id 升序锁）。
-- 规则：任何改变"文件挂在哪个条目"的写操作都先锁这个文件的 files 行（UNIQUE (file_id) 只是兜底，撞上时 409 conflict
-- "已经挂在别的条目里"）；所以只换页号的页不用锁 files 行。
BEGIN;
SELECT pg_advisory_xact_lock_shared(6002, $project_lock_key);        -- 1. 归类树的共享锁（同时确认项目未删除）
SELECT status, row_version, item_no, archive_no, title               -- 3. 锁条目行：同一个条目的页只在锁住它之后改；没有 → 404；
FROM archive_items WHERE id = $id AND project_id = $pid AND deleted_at IS NULL   -- locked → 409 item_locked（没有变化的
FOR NO KEY UPDATE;                                                   --    PUT 也一样）；row_version 不等 → 409 conflict
SELECT file_id, page_no FROM archive_item_files                      -- 现在的页 = $current；应用里和 $wanted 比出 $linked
WHERE archive_item_id = $id ORDER BY page_no;                        --   （新挂上的、新页号）、$unlinked（移出的、原页号）、
                                                                     --   $repaged（留下、页号变了的）、$moved（留下的页里按原来
                                                                     --   的顺序取最长递增子序列，其余的就是挪动过的；只是插入、
                                                                     --   移出引起的顺延不算）。都为空 → 没有变化，200、不写库
SELECT pg_advisory_xact_lock_shared(6001, $project_lock_key);        -- 4. $linked、$unlinked 不为空时才拿文件树的共享锁：和删除、
                                                                     --    移动文件夹（排他锁）互斥。只换顺序、删除没有页的条目不拿
SELECT fi.id FROM files fi                                           -- 5. 锁新挂上的和移出的文件的 files 行（只换页号的在这个条目里，
WHERE fi.id IN (SELECT ff.file_id FROM folder_files ff               --    已归类的文件不能删除，不用锁），id 升序，只锁这个项目里的。
                WHERE ff.file_id = ANY($linked || $unlinked) AND ff.project_id = $pid)   -- 和删除文件、删除文件夹、审核、重扫锁的
ORDER BY fi.id FOR NO KEY UPDATE;                                    --    是同一行：挂接和"删除已归类的文件"互斥，不会漏
SELECT ff.file_id, ff.folder_id, ff.file_name, ff.review_status,     -- 锁住之后用新语句重读（只读，日志要的名字、路径、版本号）：
       (ff.deleted_at IS NULL AND f.deleted_at IS NULL) AS live,     -- $linked 里读不到、或 live 为 false（文件、所在文件夹已删除、
       COALESCE((SELECT max(s.version_no) FROM file_snapshots s      --   不在这个项目里）→ 404 details.ids（按请求里的顺序），
                 WHERE s.file_id = ff.file_id), 0) AS version_no     --   什么都不改
FROM folder_files ff JOIN folders f ON f.id = ff.folder_id
WHERE ff.file_id = ANY($linked || $unlinked || $moved) AND ff.project_id = $pid;
SELECT af.file_id, a.id, a.category_id, a.item_no, a.archive_no,     -- 已经挂在别的条目里 → 409 file_cataloged，details
       a.title, af.page_no, a.status                                 --   {ids（file_id）, items: [{file_id, item_id, category_id,
FROM archive_item_files af JOIN archive_items a ON a.id = af.archive_item_id  --   category_names, item_no, archive_no, title, page_no,
                                                                     --   item_status}]}，category_names 同 placements 那段
WHERE af.file_id = ANY($linked) AND af.archive_item_id <> $id;       --   （一个文件只能在一个条目里，UNIQUE (file_id) 兜底）
-- 写挂接（硬性规则 4：这三条只写 archive_item_files，不碰快照和文件夹）。ux_archive_item_page 是
-- DEFERRABLE INITIALLY DEFERRED，提交时才检查，中间可以重号。
DELETE FROM archive_item_files WHERE archive_item_id = $id AND file_id = ANY($unlinked);
UPDATE archive_item_files af SET page_no = t.page_no
FROM unnest($repaged_ids::uuid[], $repaged_pages::int[]) AS t(file_id, page_no)
WHERE af.archive_item_id = $id AND af.file_id = t.file_id;
INSERT INTO archive_item_files (project_id, archive_item_id, file_id, page_no, linked_by)
SELECT $pid, $id, t.file_id, t.page_no, $user
FROM unnest($linked_ids::uuid[], $linked_pages::int[]) AS t(file_id, page_no);
INSERT INTO file_logs (project_id, file_id, action, operator_id, detail)       -- 先 unlink（按原页号）再 link（按新页号），
SELECT $pid, t.file_id, t.action, $user, t.detail                             -- 每页一条：{item_id, item_no, archive_no, title,
FROM unnest($file_ids::uuid[], $actions::text[], $details::jsonb[]) WITH ORDINALITY AS t(file_id, action, detail, n)
ORDER BY t.n;                                                        --   page_no, file_name, folder_path, version_no, review_status}，
                                                                     --   删除条目时另有 item_deleted: true
INSERT INTO audit_logs (project_id, operator_id, target_type, target_id, action, detail)    -- $moved 不为空时一条：
VALUES ($pid, $user, 'archive_item', $id, 'reorder', $detail);      -- {page_count, linked, unlinked, moved: [{file_id, file_name,
                                                                     --   from_page, to_page}]}
UPDATE archive_items SET row_version = row_version + 1, updated_by = $user, updated_at = now()
WHERE id = $id;                                                      -- 页序是条目自己的变化；删除条目时另写 deleted_at = now()，
                                                                     -- 件号不回收，档号留在行上（唯一索引只管未删除的），审计
                                                                     -- delete {before: {category_id, category_path, item_no,
                                                                     -- archive_no, title, status, page_count}}
COMMIT;                                                              -- 提交后：有挂上、移出的页时发 files.changed（这些文件直接
                                                                     -- 所在的文件夹 + file_ids，超过 500 个只带 folder_ids），再发
                                                                     -- catalog.changed {category_ids: [所在节点], item_ids: [条目]}

-- 审核的写操作（M2-1，实现见 doclite_infra::reviews）。加锁顺序（design §2.1、§2.2 的接口细化，roadmap M2 的全局约定）：
--   1. 项目文件树的共享锁（事务第一步，lock_file_tree）；
--   2.（按文件夹分配、按子树改派时）不加锁地挑出候选文件；
--   3. files 行 FOR NO KEY UPDATE，按 id 升序，只锁这个项目里的；锁住之后用新语句重新读 folder_files（是否已删除、
--      状态、所在文件夹）、当前快照和 open 的分配（下面的 $reread）；
--   4. 写 file_reviews、folder_files、file_logs（多行时一条 SQL，unnest）。
-- 不锁 folder_files 行、file_reviews 行：同一个文件上改它们的写操作（分配、改派、审核、撤回、重扫、删除文件和文件夹）
-- 都先锁 files 行，部分唯一索引 ux_file_reviews_open 兜底。folder_files 是 file_reviews、archive_item_files 外键的父表，
-- 不能用 FOR UPDATE 锁它（会挡住插行时的外键检查）。
-- 子树用区间：$lo = 选中文件夹的 path_code，$hi = 把它末尾的点换成 /（doclite_domain::tree::subtree_range）。

-- 第 3 步（每个审核写事务都一样）：
SELECT fi.id FROM files fi                                           -- 按 id 升序锁 files 行，只锁这个项目里的
WHERE fi.id IN (SELECT ff.file_id FROM folder_files ff WHERE ff.file_id = ANY($file_ids) AND ff.project_id = $pid)
ORDER BY fi.id FOR NO KEY UPDATE;
SELECT ff.file_id, ff.folder_id, ff.file_name, ff.review_status, ff.review_round, f.path_code,   -- $reread
       s.id AS snapshot_id, s.version_no,                            -- 当前快照 = max(version_no)
       r.id AS open_id, r.reviewer_id AS open_reviewer_id, u.display_name AS open_reviewer_name
FROM folder_files ff
JOIN folders f ON f.id = ff.folder_id
CROSS JOIN LATERAL (SELECT x.id, x.version_no FROM file_snapshots x WHERE x.file_id = ff.file_id
                    ORDER BY x.version_no DESC LIMIT 1) s
LEFT JOIN file_reviews r ON r.file_id = ff.file_id AND r.status = 'open'
LEFT JOIN users u ON u.id = r.reviewer_id
WHERE ff.file_id = ANY($file_ids) AND ff.project_id = $pid AND ff.deleted_at IS NULL AND f.deleted_at IS NULL
ORDER BY f.path_code, ff.sort_no, ff.file_name, ff.file_id;

-- 负责人分配审核（POST /api/projects/{pid}/review-assignments）：把选中的文件夹（含子文件夹）里所有"待分配"的文件
-- 分给某个审核员（最多 10000 个，超过 400）；按选中的文件分配时 $candidates 就是请求里的 file_ids（最多 500 个）。
-- 审核人要启用、有 review 菜单、能进这个项目（事务外先查，不合格 400）。
BEGIN;
SELECT pg_advisory_xact_lock_shared(6001, $project_lock_key);        -- 1. 项目文件树的共享锁
SELECT ff.file_id FROM folders f                                     -- 2. 候选（不加锁）= $candidates
JOIN folder_files ff ON ff.folder_id = f.id AND ff.deleted_at IS NULL
WHERE f.project_id = $pid AND f.path_code >= $lo AND f.path_code < $hi AND f.deleted_at IS NULL
  AND ff.review_status = 'unassigned';
-- 3. 锁 files 行、$reread（见上）。应用里挑出仍是 unassigned、没有 open 分配、（按文件夹时）仍在子树里的 = $picked，
--    其余计入 skipped；按选中的文件分配时有不存在、已删除、不在这个项目里的整体回滚（404，details.ids）。
INSERT INTO file_reviews (id, project_id, file_id, reviewer_id, assigned_by)  -- 4. id 由应用生成（UUIDv7）
SELECT t.id, $pid, t.file_id, $reviewer, $user FROM unnest($new_ids::uuid[], $picked::uuid[]) AS t(id, file_id);
UPDATE folder_files SET review_status = 'pending', updated_at = now() WHERE file_id = ANY($picked);
INSERT INTO file_logs (project_id, file_id, action, operator_id, detail)       -- 每个文件一条
SELECT $pid, t.file_id, 'assign_review', $user, t.detail                      -- detail：{reviewer, reviewer_id, file_name,
FROM unnest($picked::uuid[], $details::jsonb[]) AS t(file_id, detail);        --   folder_path, version_no, review_round}
COMMIT;                                                              -- 提交后发 files.changed（超过 500 个只带 folder_ids）

-- 改派（单个 POST /api/review-assignments/{id}/reassign；批量 POST /api/projects/{pid}/review-assignments:reassign）：
-- 只改派 pending 的 open 分配。单个时 $candidates 是那一行的 file_id，那一行必须仍是 open、文件仍是 pending（否则 409
-- invalid_state）；批量时是选中的 file_ids，或"某人在某文件夹子树下的全部待办"（下面的查询，最多 10000 个），
-- 不是 pending、没有 open 分配、已经分给目标审核人的跳过。
BEGIN;
SELECT pg_advisory_xact_lock_shared(6001, $project_lock_key);
SELECT r.file_id FROM file_reviews r                                 -- 按子树改派时的候选（不加锁）
JOIN folder_files ff ON ff.file_id = r.file_id AND ff.deleted_at IS NULL
JOIN folders f ON f.id = ff.folder_id AND f.deleted_at IS NULL
WHERE r.project_id = $pid AND r.status = 'open' AND r.reviewer_id = $from
  AND f.path_code >= $lo AND f.path_code < $hi;
-- 3. 锁 files 行、$reread；$picked 是符合条件的，$old_open_ids 是它们 open 的那一行
UPDATE file_reviews SET status = 'cancelled' WHERE id = ANY($old_open_ids);   -- 先取消旧行，再插新行（部分唯一索引）
INSERT INTO file_reviews (id, project_id, file_id, reviewer_id, assigned_by)
SELECT t.id, $pid, t.file_id, $to, $user FROM unnest($new_ids::uuid[], $picked::uuid[]) AS t(id, file_id);
INSERT INTO file_logs (project_id, file_id, action, operator_id, detail)       -- detail：{from, from_id, to, to_id,
SELECT $pid, t.file_id, 'reassign_review', $user, t.detail                    --   file_name, folder_path, version_no, review_round}
FROM unnest($picked::uuid[], $details::jsonb[]) AS t(file_id, detail);
COMMIT;

-- 审核员的待办（GET /api/me/reviews，M2-2，实现见 doclite_infra::review_queries，规则见 design §2.2 的"接口细化（M2-2）"）：
-- 每个文件取最近一次分给我、没有取消的那一行 = 没有 id 更大的、我的、这个项目里没取消的行（按 id 不按时间）；只含
-- 未删除的文件、未删除的文件夹；当前版本是 version_no 最大的快照。
-- - mine 的 r、n 两边都带项目条件，走 0002 的部分索引 ix_file_reviews_reviewer_project (reviewer_id, project_id)
--   WHERE status <> 'cancelled'：只碰这个人在这个项目里的行，不随他在别的项目里的历史增长。写成反连接（不用
--   DISTINCT ON … ORDER BY id DESC），PG 能选并行哈希反连接、不用排序。
-- - 当前版本按文件逐个取（LATERAL … LIMIT 1，走 (file_id, version_no) 唯一索引），不扫全部快照。
-- - base（MATERIALIZED，只算一次）只带计数、筛选、排序要用的窄列；文件名等只给这一页的行再连 folder_files、file_reviews 取。
-- - 一条 SQL 同时出按文件夹的计数（kind 0，不受 state、low_dpi 影响）和这一页（kind 1）；这一页另算 is_current：
--   这个文件上没有 id 更大的分配（不论是谁的）。
-- 范围：$lo/$hi 是 folder_id 子树的编码区间，不按文件夹时用根的区间（'00001.'、'00001/'）。
-- keyset 分页：按 (所在文件夹 path_code, sort_no, file_id) 排，$c_path/$c_sort/$c_file 是上一页最后一行的这三个值
-- （第一页为 NULL），多取一行判断有没有下一页。用未命名的预备语句执行（每次按实际参数做计划，sqlx 的 persistent(false)）：
-- 参数对行数影响很大，命名语句执行 5 次后 PG 可能换成通用计划。
WITH mine AS (
  SELECT r.id, r.file_id, r.status, r.result
  FROM file_reviews r
  WHERE r.reviewer_id = $user AND r.project_id = $pid AND r.status <> 'cancelled'
    AND NOT EXISTS (SELECT 1 FROM file_reviews n
                    WHERE n.file_id = r.file_id AND n.reviewer_id = $user AND n.project_id = $pid
                      AND n.status <> 'cancelled' AND n.id > r.id)),
base AS MATERIALIZED (
  SELECT m.id, m.file_id, m.status, m.result, ff.folder_id, ff.sort_no, f.path_code, s.version_no, s.dpi
  FROM mine m
  JOIN folder_files ff ON ff.file_id = m.file_id AND ff.deleted_at IS NULL
  JOIN folders f ON f.id = ff.folder_id AND f.deleted_at IS NULL AND f.path_code >= $lo AND f.path_code < $hi
  CROSS JOIN LATERAL (SELECT x.version_no, x.dpi FROM file_snapshots x WHERE x.file_id = m.file_id
                      ORDER BY x.version_no DESC LIMIT 1) s),
page AS (
  SELECT b.* FROM base b
  WHERE ($state = 'all' OR ($state = 'open' AND b.status = 'open') OR b.result = $state)
    AND (NOT $low_dpi OR b.dpi < 200)                                -- DPI 不足：当前版本低于 200
    AND ($c_path IS NULL OR (b.path_code, b.sort_no, b.file_id) > ($c_path, $c_sort, $c_file))
  ORDER BY b.path_code, b.sort_no, b.file_id
  LIMIT $limit + 1)
SELECT 0 AS kind, b.folder_id, min(b.path_code) AS path_code, count(*) AS n_all,
       count(*) FILTER (WHERE b.status = 'open') AS n_open,
       count(*) FILTER (WHERE b.result = 'approved') AS n_approved,
       count(*) FILTER (WHERE b.result = 'rejected') AS n_rejected,
       count(*) FILTER (WHERE b.dpi < 200) AS n_low_dpi,
       NULL::uuid AS file_id, NULL::int AS sort_no /* , …这一页的列都为 NULL */
FROM base b GROUP BY b.folder_id
UNION ALL
SELECT 1, p.folder_id, p.path_code, NULL, NULL, NULL, NULL, NULL, p.file_id, p.sort_no
       /* , ff.file_name, ff.review_round, ff.review_status, r.assigned_at, r.reviewed_at, … */,
       NOT EXISTS (SELECT 1 FROM file_reviews x WHERE x.file_id = p.file_id AND x.id > p.id) AS is_current
FROM page p
JOIN folder_files ff ON ff.file_id = p.file_id
JOIN file_reviews r ON r.id = p.id
ORDER BY kind, path_code, sort_no, file_id;
-- 文件夹路径：对计数里的文件夹 id 一次查出名字和路径（doclite_infra::files::folder_names，祖先按编码精确查）。
-- "其他项目还有 n 个待审"：$others 是我能进的其他未删除项目（admin 是全部，doclite_domain::project_access）；已归档的
-- 照样算，带上状态。文件、文件夹是否未删除逐行确认（LATERAL … LIMIT 1，不让 PG 改成哈希连接、扫一遍全部 folder_files）。
SELECT r.project_id, p.status, count(*) AS open
FROM file_reviews r
JOIN projects p ON p.id = r.project_id AND p.deleted_at IS NULL
CROSS JOIN LATERAL (SELECT 1 FROM folder_files ff JOIN folders f ON f.id = ff.folder_id
                    WHERE ff.file_id = r.file_id AND ff.deleted_at IS NULL AND f.deleted_at IS NULL
                    LIMIT 1) live
WHERE r.reviewer_id = $user AND r.status = 'open' AND r.project_id = ANY($others)
GROUP BY r.project_id, p.project_code, p.status ORDER BY p.project_code;

-- 审核分配页的其他读接口（M2-2，review_assign；同样只含未删除的文件、文件夹，keyset 分页同上）：
-- 可以分配的人（GET /api/projects/{pid}/reviewers）：条件和分配时一样（启用、有 review 菜单、能进这个项目），
-- 三列的写法和 doclite_infra::reviews::reviewer_facts_on 共用一段（reviewer_facts_columns）；带这个项目里的待办数。
WITH open AS (
  SELECT r.reviewer_id, count(*) AS open FROM file_reviews r
  JOIN folder_files ff ON ff.file_id = r.file_id AND ff.deleted_at IS NULL
  JOIN folders f ON f.id = ff.folder_id AND f.deleted_at IS NULL
  WHERE r.project_id = $pid AND r.status = 'open' GROUP BY r.reviewer_id)
SELECT u.id, u.username, u.display_name, COALESCE(o.open, 0) AS open
FROM users u LEFT JOIN open o ON o.reviewer_id = u.id
WHERE u.status = 'active'
  AND EXISTS (SELECT 1 FROM user_roles ur JOIN role_menus rm ON rm.role_id = ur.role_id
              WHERE ur.user_id = u.id AND rm.menu_code = 'review')
  AND (EXISTS (SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id WHERE ur.user_id = u.id AND r.role_code = 'admin')
    OR EXISTS (SELECT 1 FROM project_members m WHERE m.project_id = $pid AND m.user_id = u.id)
    OR EXISTS (SELECT 1 FROM project_roles pr JOIN user_roles ur ON ur.role_id = pr.role_id
               WHERE pr.project_id = $pid AND ur.user_id = u.id))
ORDER BY u.display_name, lower(u.username), u.id;
-- 按文件夹的状态计数（GET /api/projects/{pid}/review-summary）：子树里每个文件按"落在哪个直接子文件夹的子树里"分组，
-- 分组键是所在文件夹编码的前 length($path) + 6 个字符（等于 $path 的是直接放在这个文件夹里的）。
-- ff.project_id = $pid（结果不变，M3-4 审查时补上）让 PG 能用 ix_folder_files_review (project_id, review_status)
-- 只读这个项目的文件；不带时扫所有项目的 folder_files，耗时随整个系统的文件数增长。
SELECT left(f.path_code, length($path) + 6) AS bucket, ff.review_status, count(*)
FROM folders f JOIN folder_files ff ON ff.folder_id = f.id AND ff.project_id = $pid AND ff.deleted_at IS NULL
WHERE f.project_id = $pid AND f.path_code >= $lo AND f.path_code < $hi AND f.deleted_at IS NULL
GROUP BY 1, 2;
-- 归类的读接口（M3-4，实现见 doclite_infra::catalog_queries，规则见 design §2.2 的"接口细化（M3-4）"）。只读表、不加锁，
-- 一个接口的几条查询在同一个只读快照里。文件"已归类"= archive_item_files 里有它的行（和 M1 的 file_cataloged 同一个
-- 定义，不看条目的 deleted_at；不变量：已删除的条目没有挂接行，M3-3 删除条目时一并移出）；所在条目 draft、done 是
-- "已归类未定稿"，locked 是"已定稿"。
-- 看板矩阵（GET /api/projects/{pid}/catalog-summary，dashboard）：和上面的状态计数同一种分组，另外左连挂接和条目，
-- 按（分组、审核状态、条目状态）计数，item_status 为 NULL 是未归类。ff.project_id = $pid 让 PG 能用
-- ix_folder_files_review (project_id, review_status) 只读这个项目的文件；af.project_id = $pid 在哈希连接扫
-- archive_item_files 全表时只装这个项目的挂接。archive_item_files、archive_items 没有按项目的索引，耗时随全系统的
-- 挂接行数增长（规模数字和以后的两个方向见 design §2.2）。"可导出"在应用里按项目设置 export.require_locked_item 算：
-- true 时是 locked 的，false 时是全部已归类的；审核状态不影响（导出时另行勾选）。
SELECT left(f.path_code, length($path) + 6) AS bucket, ff.review_status, a.status AS item_status, count(*)
FROM folders f
JOIN folder_files ff ON ff.folder_id = f.id AND ff.project_id = $pid AND ff.deleted_at IS NULL
LEFT JOIN archive_item_files af ON af.file_id = ff.file_id AND af.project_id = $pid
LEFT JOIN archive_items a ON a.id = af.archive_item_id
WHERE f.project_id = $pid AND f.path_code >= $lo AND f.path_code < $hi AND f.deleted_at IS NULL
GROUP BY 1, 2, 3;
-- 一批文件各挂在哪个条目（POST /api/projects/{pid}/catalog-placements，最多 500 个；GET /api/files/{fid}/catalog 是
-- 一个的情况）：只取未删除、在这个项目里的文件，其余的放进 missing；没挂接的条目列为 NULL。按 file_id 逐个查
-- （folder_files、archive_item_files 的 file_id 都是唯一索引）。
SELECT q.file_id, a.id AS item_id, a.item_no, a.archive_no, a.title, af.page_no, a.status, a.category_id
FROM unnest($file_ids::uuid[]) AS q(file_id)
JOIN folder_files ff ON ff.file_id = q.file_id AND ff.project_id = $pid AND ff.deleted_at IS NULL
JOIN folders f ON f.id = ff.folder_id AND f.deleted_at IS NULL
LEFT JOIN archive_item_files af ON af.file_id = ff.file_id
LEFT JOIN archive_items a ON a.id = af.archive_item_id;
-- 这些条目所在归类节点的名称链（响应里的 category_names；category_path 是它用 / 连接，只作显示用）：从根节点的
-- 下一级起（根节点是项目本身，界面上不显示），祖先按编码精确查；根节点自己没有行（名称链为空）。
SELECT c.id, array_agg(x.category_name ORDER BY x.path_code)
FROM archive_categories c
CROSS JOIN LATERAL generate_series(12, length(c.path_code), 6) AS g(n)
JOIN archive_categories x ON x.project_id = c.project_id AND x.path_code = left(c.path_code, g.n)
WHERE c.id = ANY($category_ids)
GROUP BY c.id;

-- M3-8 验收的核对 SQL（只读，在 psql 里跑；不是接口的实现）。
-- 1. 整理不影响归类：整理文件目录（移动、改名、复制文件，移动文件夹）前后各导出一次，逐行 diff 必须完全相同
--    （硬性规则 3、4）。$lo/$hi 是归类节点（例如全宗）子树的编码区间，整个项目用根的 '00001.'、'00001/'。
--    例如 \copy (…) TO 'm3-before.csv' CSV HEADER，只做整理之后再导出 m3-after.csv，diff 两个文件（两次之间不做
--    归类的写操作：row_version 会变；审核、重扫不改它）。
SELECT c.path_code, a.id AS item_id, a.item_no, a.archive_no, a.status, a.row_version,
       af.page_no, af.file_id, af.linked_by, af.linked_at
FROM archive_categories c
JOIN archive_items a ON a.category_id = c.id AND a.deleted_at IS NULL
JOIN archive_item_files af ON af.archive_item_id = a.id
WHERE c.project_id = $pid AND c.path_code >= $lo AND c.path_code < $hi AND c.deleted_at IS NULL
ORDER BY c.path_code, a.item_no, a.id, af.page_no;
--    同时必须为空的两条（M3-3 的不变量）：已删除的条目有挂接行；条目的页号不是连续的 1..n。
SELECT af.* FROM archive_item_files af JOIN archive_items a ON a.id = af.archive_item_id
WHERE af.project_id = $pid AND a.deleted_at IS NOT NULL;
SELECT archive_item_id, count(*), min(page_no), max(page_no) FROM archive_item_files
WHERE project_id = $pid GROUP BY 1 HAVING min(page_no) <> 1 OR max(page_no) <> count(*);
-- 2. 还原一个文件的全过程（$file）：文件日志（按 id，即发生的先后）、快照、审核分配、现在挂在哪，外加条目审计里
--    这个文件的重排（reorder 不写 file_logs）。显示名是现在的；日志 detail 里存的是当时的名字。
SELECT l.id, l.created_at, l.action, u.display_name AS operator,
       l.detail - '{item_id,reviewer_id,from_id,to_id,source_file_id,source_snapshot_id}'::text[] AS detail
FROM file_logs l JOIN users u ON u.id = l.operator_id
WHERE l.file_id = $file ORDER BY l.id;
SELECT s.version_no, s.snapshot_type, s.created_at, u.display_name AS created_by, s.upload_name, s.dpi
FROM file_snapshots s LEFT JOIN users u ON u.id = s.created_by       -- created_by 为 NULL 是系统
WHERE s.file_id = $file ORDER BY s.version_no;
SELECT r.id, r.assigned_at, ab.display_name AS assigned_by, rv.display_name AS reviewer, r.status, r.result,
       s.version_no AS reviewed_version, r.reject_reasons, r.comment, r.reviewed_at
FROM file_reviews r
JOIN users rv ON rv.id = r.reviewer_id JOIN users ab ON ab.id = r.assigned_by
LEFT JOIN file_snapshots s ON s.id = r.snapshot_id                   -- 审的是哪一版（没审的为 NULL）
WHERE r.file_id = $file ORDER BY r.id;                               -- 按 id 即分配的先后（含已取消的）
SELECT ff.file_name, ff.review_status, ff.review_round, ff.deleted_at, af.page_no,
       a.id AS item_id, a.item_no, a.archive_no, a.status AS item_status, a.row_version
FROM folder_files ff
LEFT JOIN archive_item_files af ON af.file_id = ff.file_id           -- 没挂接时后几列为 NULL
LEFT JOIN archive_items a ON a.id = af.archive_item_id
WHERE ff.file_id = $file;
SELECT g.created_at, g.target_id AS item_id, m->>'from_page' AS from_page, m->>'to_page' AS to_page
FROM audit_logs g CROSS JOIN LATERAL jsonb_array_elements(g.detail->'moved') m
WHERE g.target_type = 'archive_item' AND g.action = 'reorder' AND m->>'file_id' = $file::text
  AND g.target_id IN (SELECT (detail->>'item_id')::uuid FROM file_logs
                      WHERE file_id = $file AND action IN ('link', 'unlink'))   -- 挂过的条目（走 ix_audit_logs_target）
ORDER BY g.id;

-- open 分配（GET /api/projects/{pid}/review-assignments，改派用）：这一页；审核人汇总另一条（按 reviewer_id 计数，
-- 带 reviewer_facts_columns 的三列，标出不能再审的：停用、没有审核菜单、失去项目授权）。
SELECT r.id, r.file_id, ff.id, ff.folder_id, ff.file_name, ff.sort_no, ff.review_round, f.path_code,
       r.reviewer_id, u.display_name, r.assigned_by, r.assigned_at
FROM file_reviews r
JOIN folder_files ff ON ff.file_id = r.file_id AND ff.deleted_at IS NULL
JOIN folders f ON f.id = ff.folder_id AND f.deleted_at IS NULL AND f.path_code >= $lo AND f.path_code < $hi
JOIN users u ON u.id = r.reviewer_id
WHERE r.project_id = $pid AND r.status = 'open'
  AND ($reviewer IS NULL OR r.reviewer_id = $reviewer)
  AND ($c_path IS NULL OR (f.path_code, ff.sort_no, ff.file_id) > ($c_path, $c_sort, $c_file))
ORDER BY f.path_code, ff.sort_no, ff.file_id LIMIT $limit + 1;
-- 待重扫（GET /api/projects/{pid}/rescans，scan 或 files）：rejected 的文件、文件夹备注（原件位置）、当前版本号、
-- 最近一次驳回（按 id）和它审的版本。
SELECT ff.file_id, ff.folder_id, ff.file_name, f.path_code, f.remark, cur.version_no,
       rj.reviewer_id, u.display_name, rj.reject_reasons, rj.comment, rs.version_no AS rejected_version_no
FROM folder_files ff
JOIN folders f ON f.id = ff.folder_id AND f.deleted_at IS NULL AND f.path_code >= $lo AND f.path_code < $hi
CROSS JOIN LATERAL (SELECT x.version_no FROM file_snapshots x WHERE x.file_id = ff.file_id
                    ORDER BY x.version_no DESC LIMIT 1) cur
LEFT JOIN LATERAL (SELECT r.* FROM file_reviews r WHERE r.file_id = ff.file_id AND r.status = 'done'
                   AND r.result = 'rejected' ORDER BY r.id DESC LIMIT 1) rj ON true
LEFT JOIN users u ON u.id = rj.reviewer_id
LEFT JOIN file_snapshots rs ON rs.id = rj.snapshot_id
WHERE ff.project_id = $pid AND ff.review_status = 'rejected' AND ff.deleted_at IS NULL
ORDER BY f.path_code, ff.sort_no, ff.file_id LIMIT $limit + 1;
-- 一个文件的全部分配和结果（GET /api/files/{fid}/reviews）：含已取消的，按 id（分配的先后），带所审快照的版本号。
SELECT r.*, u.display_name AS reviewer_name, a.display_name AS assigned_by_name, s.version_no
FROM file_reviews r JOIN users u ON u.id = r.reviewer_id JOIN users a ON a.id = r.assigned_by
LEFT JOIN file_snapshots s ON s.id = r.snapshot_id
WHERE r.file_id = $file ORDER BY r.id;

-- 审核（POST /api/files/{fid}/review）：通过或驳回，填上结果和审核时的快照。只能处理分给自己的 open 分配。
BEGIN;
SELECT pg_advisory_xact_lock_shared(6001, $project_lock_key);        -- 先拿项目文件树的共享锁（design §2.1、§2.2）
-- 3. 锁这个文件的 files 行（files 只有 id，正好当作每个文件的行锁；不挡外键检查）、$reread。依次检查：
--    不是 pending → 409 invalid_state；open 的那一行不是 $user 的 → 403 not_assignee；
--    带了审核员看到的 $seen_snapshot 且不是当前快照 → 409 conflict。
UPDATE file_reviews SET status = 'done', result = $result, snapshot_id = $head_snapshot,
       reject_reasons = $reasons, comment = $comment, reviewed_at = now()       -- 通过时 $reasons 为空数组
WHERE id = $open_id AND status = 'open';
UPDATE folder_files SET review_status = $result, updated_at = now() WHERE file_id = $file;  -- approved 或 rejected
INSERT INTO file_logs (project_id, file_id, action, operator_id, detail)
VALUES ($pid, $file, $result_action, $user, $detail);                -- approve {reviewer, file_name, folder_path, version_no,
COMMIT;                                                              --   review_round, comment?}；reject 另有 reasons、comment

-- 批量通过（POST /api/projects/{pid}/reviews:approve，最多 500 个）：一个事务，锁住全部之后检查，任何一个不合规则
-- 就整体回滚，details.ids 列出是哪些；否则每张表一条 SQL 写完。
BEGIN;
SELECT pg_advisory_xact_lock_shared(6001, $project_lock_key);        -- 1. 项目文件树的共享锁
SELECT fi.id FROM files fi                                           -- 3. 按 id 升序锁 files 行，只锁这个项目里的
WHERE fi.id IN (SELECT ff.file_id FROM folder_files ff WHERE ff.file_id = ANY($file_ids) AND ff.project_id = $pid)
ORDER BY fi.id FOR NO KEY UPDATE;
-- $reread（见上）。依次检查：不在结果里的（不存在、已删除、不在这个项目里）→ 404；review_status 不是 pending →
-- 409 invalid_state；open_reviewer_id 不是 $user → 403 not_assignee。$open_ids、$head_snapshots 来自 $reread。
UPDATE file_reviews r SET status = 'done', result = 'approved', snapshot_id = t.snapshot_id,
       reject_reasons = '{}', comment = NULL, reviewed_at = now()
FROM unnest($open_ids::uuid[], $head_snapshots::uuid[]) AS t(id, snapshot_id)
WHERE r.id = t.id AND r.status = 'open';                             -- 更新的行数要等于文件数
UPDATE folder_files SET review_status = 'approved', updated_at = now() WHERE file_id = ANY($file_ids);
INSERT INTO file_logs (project_id, file_id, action, operator_id, detail)       -- 每个文件一条 approve
SELECT $pid, t.file_id, 'approve', $user, t.detail                            -- detail：{reviewer, file_name, folder_path,
FROM unnest($file_ids::uuid[], $details::jsonb[]) AS t(file_id, detail);      --   version_no, review_round, batch: true}
COMMIT;                                                              -- 提交后发 files.changed（超过 500 个只带 folder_ids）

-- 撤回通过（POST /api/files/{fid}/reopen）：approved → pending，给通过它的审核员新增一行 open。
BEGIN;
SELECT pg_advisory_xact_lock_shared(6001, $project_lock_key);
-- 3. 锁 files 行、$reread：不是 approved → 409 invalid_state
SELECT r.reviewer_id, r.assigned_by, u.display_name
FROM file_reviews r JOIN users u ON u.id = r.reviewer_id             -- 通过它的那一行（最近一次通过）：按 id 取，
WHERE r.file_id = $file AND r.status = 'done' AND r.result = 'approved'   -- UUIDv7 在锁住 files 行之后生成，比 reviewed_at 稳
ORDER BY r.id DESC LIMIT 1;                                          -- 只有 review 菜单的人：不是自己 → 403 not_assignee
-- 他还能不能审这个项目：和分配时的三个条件一样（启用、有 review 菜单、能进这个项目，doclite_infra::reviews::
-- reviewer_facts_on）。不能时不插，状态改为 unassigned，日志记 skipped_reviewer。重扫的再分配也一样。
INSERT INTO file_reviews (id, project_id, file_id, reviewer_id, assigned_by)
VALUES ($id, $pid, $file, $reviewer, $assigned_by)                   -- assigned_by 沿用通过的那一行
ON CONFLICT (file_id) WHERE status = 'open' DO NOTHING;
UPDATE folder_files SET review_status = 'pending', updated_at = now() WHERE file_id = $file;  -- 没有 open 时 'unassigned'
INSERT INTO file_logs (project_id, file_id, action, operator_id, detail)
VALUES ($pid, $file, 'reopen', $user, $detail);                      -- {by, reviewer, reviewer_id, file_name, folder_path,
COMMIT;                                                              --   version_no, review_round}（没分配出去时另有 skipped_reviewer）

-- 重扫（POST /api/files/{fid}/rescan，M1-7、M2-1；实现见 doclite_infra::ingest 的 insert_rescan，规则见 design §1.5、§4.3）：
-- 只接受 rejected 的文件。事务外：读完请求体和文件头、预判查重；拿进程内按 file_id 的锁（包住"定版本号 $version →
-- 传 S3 → 写库"），把原图、封面传到 v<n>.<ext>、v<n>.cover.jpg；再开一个短事务（M2-8 按代码补上这一段）：
BEGIN;
SELECT pg_advisory_xact_lock_shared(6001, $project_lock_key);        -- 1. 项目文件树的共享锁
SELECT id FROM files WHERE id = $file FOR NO KEY UPDATE;             -- 3. 锁 files 行（没有 → 404）
SELECT ff.id, ff.folder_id, ff.file_name, ff.review_status,          -- 锁住之后重新读：未删除、所在文件夹未删除
       (SELECT max(s.version_no) FROM file_snapshots s WHERE s.file_id = ff.file_id) AS max_version
FROM folder_files ff JOIN folders f ON f.id = ff.folder_id
WHERE ff.file_id = $file AND ff.project_id = $pid AND ff.deleted_at IS NULL AND f.deleted_at IS NULL;
-- 不是 rejected → 409 invalid_state；max_version + 1 不等于 $version → 409 conflict（不用 invalid_state，免得客户端以为
-- 上一次已经成功）。然后存储 FOR KEY SHARE（仍是 active 且仍是默认）、内容锁 pg_advisory_xact_lock(6003, …)、
-- 锁内再查一次重（上面"上传前查重"的第 2 条，$self = $file：和自己现在的版本相同也是 409 duplicate_content）。
INSERT INTO file_snapshots (id, file_id, version_no, snapshot_type, storage_profile_id, storage_key, cover_key,
                            content_sha256, file_size, mime_type, upload_name, width_px, height_px, dpi, color_mode, created_by,
                            created_at)
VALUES ($snapshot, $file, $version, 'rescan', $default_profile, $key, $cover_key,
        $sha, $size, $mime, $upload_name, $width, $height, $dpi, $color_mode, $user,
        clock_timestamp());                                          -- 插入这一刻，不用事务开始的 now()（M3-2 审查）：这个事务
                                                                     -- 可能在 6001 上等了好几秒，期间条目定稿了，用 now() 会早于
                                                                     -- 定稿时间，"定稿后有新版本"漏标（design §2.3）
SELECT r.reviewer_id, r.assigned_by, u.display_name                  -- 最近一次驳回它的那一行（按 id，不按 reviewed_at）
FROM file_reviews r JOIN users u ON u.id = r.reviewer_id
WHERE r.file_id = $file AND r.status = 'done' AND r.result = 'rejected'
ORDER BY r.id DESC LIMIT 1;
-- review.rescan_reassign = same_reviewer：他还能审这个项目（启用、有 review 菜单、能进项目，reviewer_facts_on）才分回去；
-- 不能时不插，下面的状态是 unassigned，日志记 skipped_reviewer。
INSERT INTO file_reviews (id, project_id, file_id, reviewer_id, assigned_by)
VALUES ($id, $pid, $file, $reviewer, $assigned_by)                   -- assigned_by 沿用驳回的那一行
ON CONFLICT (file_id) WHERE status = 'open' DO NOTHING;              -- 万一已有 open 分配（数据异常）就不再加
SELECT r.reviewer_id, u.display_name FROM file_reviews r             -- 实际 open 的那一行：日志和响应写它的审核人
JOIN users u ON u.id = r.reviewer_id WHERE r.file_id = $file AND r.status = 'open';
UPDATE folder_files SET review_status = $status, review_round = review_round + 1, updated_at = now()
WHERE id = $folder_file RETURNING review_round;                      -- 有 open 的分配是 'pending'，没有是 'unassigned'
INSERT INTO file_logs (project_id, file_id, action, operator_id, detail)
VALUES ($pid, $file, 'rescan', $user, $detail);                      -- {folder, folder_path, file_name, upload_name, version_no,
COMMIT;                                                              --   review_round, review_status, reviewer|null}，没分配出去时
                                                                     --   另有 skipped_reviewer；提交后发 files.changed

-- 删除文件夹（软删除，DELETE /api/folders/{id}）：全部在一个事务里，按 design §2.1 的顺序加锁；
-- 已归类检查在拿到排他锁、锁住 files 行之后做，有就整体拒绝（409 file_cataloged），什么都不改。挂接（M3-3）拿着
-- 6001 共享锁、锁住 files 行之后才查文件和所在文件夹是否已删除，和这里的排他锁互斥，两边并发也不会漏。
-- $lo、$hi 是子树的编码区间：$lo = 文件夹的 path_code，$hi = 把它末尾的点换成 /（doclite_domain::tree::subtree_range）。
BEGIN;
SELECT pg_advisory_xact_lock(6001, $project_lock_key);               -- 1. 项目文件树的排他锁，事务第一步
SELECT id FROM files WHERE id IN (                                   -- 3. 按 id 升序锁住子树里未删除的文件的 files 行
  SELECT ff.file_id FROM folder_files ff JOIN folders f ON f.id = ff.folder_id
  WHERE f.project_id = $pid AND f.path_code >= $lo AND f.path_code < $hi AND ff.deleted_at IS NULL)
ORDER BY id FOR NO KEY UPDATE;
SELECT count(*) FROM folder_files ff                                 -- 大于 0 就回滚，提示先把文件移出条目
JOIN folders f ON f.id = ff.folder_id
JOIN archive_item_files af ON af.file_id = ff.file_id
WHERE f.project_id = $pid AND f.path_code >= $lo AND f.path_code < $hi AND ff.deleted_at IS NULL;
SELECT id FROM folders                                               -- 4. 按 id 升序锁住子树里未删除的文件夹行
WHERE project_id = $pid AND path_code >= $lo AND path_code < $hi AND deleted_at IS NULL
ORDER BY id FOR NO KEY UPDATE;
UPDATE folders SET deleted_at = now(), deleted_by = $user
WHERE project_id = $pid AND path_code >= $lo AND path_code < $hi AND deleted_at IS NULL
RETURNING id;                                                        -- = $folder_ids
UPDATE folder_files SET deleted_at = now(), deleted_by = $user
WHERE folder_id = ANY($folder_ids) AND deleted_at IS NULL
RETURNING file_id, file_name, folder_id;                             -- = $file_ids 和写日志要的名字
UPDATE file_reviews SET status = 'cancelled' WHERE status = 'open' AND file_id = ANY($file_ids);
INSERT INTO file_logs (project_id, file_id, action, operator_id, detail)       -- 每个文件一条 delete 日志（一条 INSERT）
SELECT $pid, t.file_id, 'delete', $user, t.detail                             -- detail：{file_name, folder, folder_path}
FROM unnest($file_ids, $details) AS t(file_id, detail);
INSERT INTO audit_logs (project_id, operator_id, target_type, target_id, action, detail)
VALUES ($pid, $user, 'folder', $folder, 'delete', $before_after);
COMMIT;                                                              -- 提交后发 folders.changed 和 files.changed

-- 导出范围（M4-1；实现见 doclite_infra::export_tasks，规则见 design §6 的"导出任务（M4-1）"）：scope_type = category 时
-- 是这些归类节点（1–5000 个，都要是本项目未删除的，否则 404 details.ids）子树里所有未删除的条目，子树重叠时只算一次；
-- $lo[]/$hi[] 是各节点的区间（$lo = path_code，$hi = 把末尾的点换成 /，doclite_domain::tree::subtree_range）。
-- scope_type = item 时直接是这些条目（未删除、本项目的，否则 404）。
SELECT a.id FROM archive_items a
WHERE a.project_id = $pid AND a.deleted_at IS NULL
  AND a.category_id IN (
    SELECT c.id FROM archive_categories c
    JOIN unnest($lo::text[], $hi::text[]) AS r(lo, hi) ON c.path_code >= r.lo AND c.path_code < r.hi
    WHERE c.project_id = $pid AND c.deleted_at IS NULL);

-- 导出预检（POST /api/projects/{pid}/export-tasks:precheck，M4-1）：范围里每个条目的页数、没通过的页数和前 5 个没通过的
-- 页，按导出顺序（所在节点的 path_code、件号、id：归类节点的 sort_no 就是 path_code 的最后一段，所以 path_code 的顺序就是
-- 树上显示的顺序）。页 = 挂接行里文件和所在文件夹都未删除的（"已归类" = 有挂接行，M3-4）。在一个
-- REPEATABLE READ READ ONLY 事务里读（项目设置 export.require_locked_item 也在这个快照里读）。
-- 就绪（用户决定 Q7，在应用里判断，doclite_domain::export_tasks::not_ready_reasons）：require_locked_item 时
-- status = 'locked'；archive_no 不为空；page_count >= 1；勾了"只导出审核通过的"时 unapproved = 0。
WITH pages AS (
  SELECT af.archive_item_id AS item_id, af.page_no, af.file_id, ff.file_name, ff.review_status
  FROM archive_item_files af
  JOIN folder_files ff ON ff.file_id = af.file_id AND ff.deleted_at IS NULL
  JOIN folders f ON f.id = ff.folder_id AND f.deleted_at IS NULL
  WHERE af.project_id = $pid AND af.archive_item_id = ANY($item_ids)
), agg AS (
  SELECT item_id, count(*) AS page_count,
         count(*) FILTER (WHERE review_status <> 'approved') AS unapproved,
         (array_agg(page_no ORDER BY page_no) FILTER (WHERE review_status <> 'approved'))[1:5] AS ex_page_no
         -- 另外三列同样写法：file_id、file_name、review_status
  FROM pages GROUP BY item_id
)
SELECT a.id, a.category_id, a.item_no, a.archive_no, a.title, a.extra_fields, a.status, a.row_version,
       COALESCE(g.page_count, 0) AS page_count, COALESCE(g.unapproved, 0) AS unapproved, g.ex_page_no
FROM archive_items a
JOIN archive_categories c ON c.id = a.category_id
LEFT JOIN agg g ON g.item_id = a.id
WHERE a.project_id = $pid AND a.id = ANY($item_ids) AND a.deleted_at IS NULL
ORDER BY c.path_code, a.item_no, a.id;
-- 名称链（category_names）同 M3-4 placements 的写法（catalog_queries::category_name_chains）。

-- 建导出任务（POST /api/projects/{pid}/export-tasks，M4-1，用户决定 Q4 冻结）：一个 REPEATABLE READ 事务，第一条语句
-- 定下快照，之后的读和 INSERT … SELECT 都在这个快照里。不拿 6002、6001，不锁归类的行、files 行：只插新行；外键检查对
-- archive_items、file_snapshots、projects、users 的行拿 FOR KEY SHARE，和归类、审核的 FOR NO KEY UPDATE、不改"键列"
-- 的 UPDATE 不冲突（REPEATABLE READ 下对快照之后被改过、没改键列的行拿 KEY SHARE 也不报串行化错误）。键列 = 这四张表上
-- 完整的唯一约束、唯一索引（不是部分索引、不是表达式索引）里的全部列，例如 archive_items 的 UNIQUE (id, project_id)
-- 里的 project_id：并发事务改了被引用的行的键列，这里就报 40001（M4-1 审查用 psql 实测）。现在没有代码改这些列；以后给
-- 这四张表的可变列加完整的唯一约束时，这个前提就不成立了（design §2.1 的"导出任务"）。万一 40001 整个重来，最多 3 次。
BEGIN ISOLATION LEVEL REPEATABLE READ;
SELECT settings FROM projects WHERE id = $pid AND deleted_at IS NULL;   -- 第一条语句：快照从这里定下；没有 → 404
-- 补交（resubmit_of，和 scope_ids 二选一）：先在这个快照里按那个任务算下面的"交付后变更"（任务要在本项目、是 done），
-- 取 resubmit_item_ids（有变更、没删除的）当条目范围，0 件 → 409 invalid_state；不受 5000 个的限制。
-- 范围 → 上面的"导出预检"；不就绪的处理（在应用里，doclite_domain::export_tasks::check_creatable）：范围里没有条目、
-- 一件就绪的都没有、only_ready = false 而有不就绪的、或者要导出的页数超过 20 万（MAX_TASK_PAGES）→ 回滚，
-- 409 invalid_state，details = 预检的结果。
INSERT INTO export_tasks (id, project_id, scope_type, scope_ids, export_format, only_approved, status, job_id, created_by)
VALUES ($task, $pid, $scope_type, $scope_ids, $format, $only_approved, 'queued', $job, $user);
-- 冻结：就绪条目的页、每页当时的最新快照（max(version_no) 那一行），页号按挂接的页号排成 1..n。页只从这些条目的
-- 挂接行来，没归类的文件不可能进来（硬性规则 6）。写进去的行数要等于同一个快照里预检数出的页数，否则回滚（500）。
INSERT INTO export_task_files (export_task_id, archive_item_id, file_id, snapshot_id, page_no)
SELECT $task, p.archive_item_id, p.file_id, s.id,
       row_number() OVER (PARTITION BY p.archive_item_id ORDER BY p.page_no)
FROM (SELECT af.archive_item_id, af.file_id, af.page_no
      FROM archive_item_files af
      JOIN folder_files ff ON ff.file_id = af.file_id AND ff.deleted_at IS NULL
      JOIN folders f ON f.id = ff.folder_id AND f.deleted_at IS NULL
      WHERE af.project_id = $pid AND af.archive_item_id = ANY($ready_item_ids)
        AND (NOT $only_approved OR ff.review_status = 'approved')) p
CROSS JOIN LATERAL (SELECT x.id FROM file_snapshots x WHERE x.file_id = p.file_id
                    ORDER BY x.version_no DESC LIMIT 1) s;
-- "当时的状态"放在 payload（不改表；格式见 doclite_domain::export_tasks::job_payload）：{export_task_id, payload_version,
-- only_ready, require_locked_item, item_count, page_count, skipped_item_count, scope_categories, item_fields,
-- items: [{id, category_id, category_names, item_no, archive_no, title, extra_fields, status, row_version, page_count}]}，
-- items 按导出顺序。M4-3 生成目录、PDF 用这里的条目信息，交付后变更拿它和现在的比。
INSERT INTO background_jobs (id, project_id, job_type, payload, status, progress_total, created_by)
VALUES ($job, $pid, 'export', $payload, 'queued', $page_count, $user);
INSERT INTO audit_logs (project_id, operator_id, target_type, target_id, action, detail)
VALUES ($pid, $user, 'export_task', $task, 'create', $after);   -- {after: {scope_type, scope_ids, export_format, only_approved,
COMMIT;                                                         --   only_ready, require_locked_item, item_count, page_count,
                                                                --   skipped_item_count, job_id, status}}；提交后发 export.changed

-- 任务列表（GET /api/projects/{pid}/export-tasks，M4-1）：最新的在前，keyset（created_at、id）。payload 只取
-- payload - 'items'（件数、页数、范围的名称链……），不把冻结的条目带回来。进度、错误在 job 上；任务 canceled 而 job 还 running 是"正在清理"。
SELECT t.*, u.display_name, j.status AS job_status, j.progress_done, j.progress_total, j.attempts, j.max_attempts,
       j.error_message, j.started_at, j.payload - 'items' AS header,
       (t.expires_at IS NOT NULL AND t.expires_at <= now()) AS expired
FROM export_tasks t
LEFT JOIN background_jobs j ON j.id = t.job_id
LEFT JOIN users u ON u.id = t.created_by
WHERE t.project_id = $pid AND ($c_at IS NULL OR (t.created_at, t.id) < ($c_at, $c_id))
ORDER BY t.created_at DESC, t.id DESC LIMIT $limit + 1;                -- ix_export_tasks_project (project_id, created_at DESC)

-- 明细（GET /api/export-tasks/{id}/files，M4-1）：按条目在导出里的序号（payload 的 items 的位置）、页号，keyset。
WITH items AS (
  SELECT (e.value->>'id')::uuid AS item_id, e.ord AS seq, (e.value->>'item_no')::int AS item_no,
         e.value->>'archive_no' AS archive_no, e.value->>'title' AS title
  FROM background_jobs j
  CROSS JOIN LATERAL jsonb_array_elements(j.payload->'items') WITH ORDINALITY AS e(value, ord)
  WHERE j.id = $job
)
SELECT i.seq, i.item_id, i.item_no, i.archive_no, i.title, x.page_no, x.file_id, x.snapshot_id,
       s.version_no, s.mime_type, s.file_size, s.content_sha256, s.upload_name, ff.file_name
FROM export_task_files x
JOIN items i ON i.item_id = x.archive_item_id
JOIN file_snapshots s ON s.id = x.snapshot_id
LEFT JOIN folder_files ff ON ff.file_id = x.file_id
WHERE x.export_task_id = $task AND ($c_seq IS NULL OR (i.seq, x.page_no) > ($c_seq, $c_page))
ORDER BY i.seq, x.page_no LIMIT $limit + 1;

-- 交付后变更（GET /api/export-tasks/{id}/changes，M4-1，用户决定 Q6）：只对 done 的任务（否则 409 invalid_state）。
-- 一个 REPEATABLE READ READ ONLY 事务里读下面三条和 payload，原因在应用里算（doclite_domain::export_tasks::compute_changes）：
-- 有页出了新版本（还在这个条目里、version_now > 导出的版本）；页被移出（item_now 为 NULL）、移到别的条目
-- （item_now 是别的条目）、页序变了（留下的页按导出的页号排，page_now 不是递增的：取最长递增子序列，其余是挪过的，
-- 插入、移出引起的顺延不算）；条目新加了页（第二条）；勾了"只导出审核通过的"而还在这个条目里的导出页现在不是
-- approved；题名、档号、著录（extra_fields 按 key，空串和没有算一样）和 payload 里的不一样；条目已删除（只报这一条）。
-- 1. 导出的每一页和那个文件现在的样子：
SELECT x.archive_item_id, x.file_id, x.page_no, s.version_no AS exported_version,
       af.archive_item_id AS item_now, af.page_no AS page_now,
       (SELECT max(v.version_no) FROM file_snapshots v WHERE v.file_id = x.file_id) AS version_now,
       CASE WHEN ff.deleted_at IS NULL AND f.deleted_at IS NULL THEN ff.review_status END AS review_status,
       ff.file_name
FROM export_task_files x
JOIN file_snapshots s ON s.id = x.snapshot_id
LEFT JOIN archive_item_files af ON af.file_id = x.file_id
LEFT JOIN folder_files ff ON ff.file_id = x.file_id
LEFT JOIN folders f ON f.id = ff.folder_id
WHERE x.export_task_id = $task;
-- 2. 导出的条目里现在有、导出时不在这个条目里的页：
SELECT af.archive_item_id, af.file_id, af.page_no, ff.file_name, ff.review_status
FROM archive_item_files af
LEFT JOIN folder_files ff ON ff.file_id = af.file_id
WHERE af.archive_item_id = ANY($exported_item_ids)
  AND NOT EXISTS (SELECT 1 FROM export_task_files x WHERE x.export_task_id = $task
                  AND x.file_id = af.file_id AND x.archive_item_id = af.archive_item_id);
-- 3. 导出的条目现在的样子（含已删除的），以及页被移到的那些条目的件号、档号：
SELECT id, deleted_at IS NOT NULL AS deleted, item_no, archive_no, title, extra_fields, status
FROM archive_items WHERE id = ANY($exported_item_ids);

-- 取消导出任务（POST /api/export-tasks/{id}/cancel，M4-1）。加锁顺序：background_jobs 行 → export_tasks 行（M4-3 的执行器
-- 领取时先 FOR UPDATE SKIP LOCKED 锁 job 行再改任务行，同一个顺序）。
BEGIN;
SELECT job_id FROM export_tasks WHERE id = $task AND project_id = $pid;          -- 没有 → 404
SELECT status FROM background_jobs WHERE id = $job FOR NO KEY UPDATE;
SELECT status FROM export_tasks WHERE id = $task FOR NO KEY UPDATE;
-- 任务 done、failed、canceled，或者 job 已经 canceled（含"任务 queued、job canceled"）→ 409 invalid_state
-- （"任务{状态}，不能取消"）。job queued（含重试前的等待）→ 两边都取消：
UPDATE background_jobs SET status = 'canceled', finished_at = now() WHERE id = $job;
UPDATE export_tasks SET status = 'canceled', finished_at = now() WHERE id = $task;
-- job running（M4-1 审查，总控决定）→ 只取消任务（上面第二条），job 保持 running；响应 cleaning_up = true。M4-3 的执行器
-- 在心跳或页与页之间查任务状态，发现 canceled 就停下、删掉已上传的部分，再
--   UPDATE background_jobs SET status = 'canceled', finished_at = now() WHERE id = $job;
-- 执行器改任务时一律带条件（UPDATE export_tasks SET status = 'done', … WHERE id = $task AND status = 'running'），改不到
-- 行就按取消处理（删掉刚上传的导出物）。标准的心跳超时回收照样接得住 running 的 job。
INSERT INTO audit_logs (project_id, operator_id, target_type, target_id, action, detail)
VALUES ($pid, $user, 'export_task', $task, 'cancel', $before_after);
COMMIT;                                                                           -- 提交后发 export.changed

-- 保存修图（插件阶段）：审核之后也可以改，状态不变；先把插件的输出（新图和插件自己生成的封面，design §9）写进默认存储，再开一个很短的事务
BEGIN;
SELECT pg_advisory_xact_lock_shared(6001, $project_lock_key);        -- 先拿项目文件树的共享锁（design §2.1、§2.2）
SELECT id FROM files WHERE id = $file FOR NO KEY UPDATE;             -- 不挡往 file_snapshots、file_logs 插行时的外键检查
SELECT max(version_no) FROM file_snapshots WHERE file_id = $file;    -- 应用层校验：等于前端基于的版本号，否则返回 409
INSERT INTO file_snapshots (id, file_id, version_no, snapshot_type, base_snapshot_id, parent_snapshot_id,
                            edit_ops, snapshot_name, storage_profile_id, storage_key, cover_key,
                            content_sha256, file_size, mime_type, created_by)
VALUES ($new, $file, $head_no + 1, 'edit', $base, $head, $ops, $name, $profile, $key, $cover_key, $sha, $size, $mime, $user);
INSERT INTO file_logs (project_id, file_id, action, operator_id, detail)
VALUES ($pid, $file, 'edit', $user, jsonb_build_object('version', $head_no + 1));
COMMIT;

-- 按目录导出（运维命令 export-tree）要用的清单：每个文件当前版本的目录路径和对象 key
SELECT (SELECT string_agg(a.folder_name, '/' ORDER BY a.path_code) FROM folders a
        WHERE a.project_id = f.project_id AND starts_with(f.path_code, a.path_code)) || '/' || ff.file_name AS export_path,
       s.storage_key
FROM folder_files ff
JOIN folders f ON f.id = ff.folder_id
JOIN LATERAL (SELECT * FROM file_snapshots x WHERE x.file_id = ff.file_id ORDER BY version_no DESC LIMIT 1) s ON true
WHERE ff.project_id = $pid AND ff.deleted_at IS NULL;

-- 后台任务（M4-3，doclite_infra::jobs、doclite_infra::export_run；design §6 的"后台任务（M4-3）""导出的执行（M4-3）"）。
-- 第一版只有导出（job_type = 'export'，业务对象是 export_tasks，id 在 payload 的 export_task_id）。
-- 加锁顺序和取消接口一样：job 行 → 任务行。对任务的改写一律带 WHERE status = 'running'（取消过的不会被覆盖）。
-- $w 是这次领取写进 runner_id 的值（主机名:进程号:随机/每次领取的随机串），$n 是这次执行的 attempts（领取后的值），
-- 两者一起叫"租约"。

-- 1. 领取（一个短事务）。6004 只让各执行器"数 running、挑一个"排队，保证全局 running 不超过 JOB_CONCURRENCY；
--    用 try，拿不到说明别的执行器正在领，这一轮不领、不等。FOR UPDATE SKIP LOCKED 是有意的（硬性规则 13、
--    design §2.1 的例外），不要改成 FOR NO KEY UPDATE。
BEGIN;
SELECT pg_try_advisory_xact_lock(6004, 0);                       -- false → ROLLBACK，这一轮不领
SELECT count(*) FROM background_jobs WHERE status = 'running';    -- >= $max → ROLLBACK，不领（正在清理的也算）
SELECT id FROM background_jobs
WHERE status = 'queued' AND run_after <= now() AND job_type = ANY($types)
ORDER BY priority DESC, run_after LIMIT 1
FOR UPDATE SKIP LOCKED;                                           -- ix_background_jobs_ready；没有 → ROLLBACK
UPDATE background_jobs SET status = 'running', runner_id = $w, started_at = now(), heartbeat_at = now(),
       attempts = attempts + 1, progress_done = 0, error_message = NULL, finished_at = NULL
WHERE id = $job
RETURNING id, job_type, project_id, payload, attempts, max_attempts, progress_total, created_by,
          payload->>'export_task_id';
SELECT status FROM export_tasks WHERE id = $task FOR NO KEY UPDATE;
-- queued（或上次执行留下的 running）→ 改成 running；canceled → job 也改成 canceled（不执行）；不存在、done、failed →
-- job failed（不应该发生）。
UPDATE export_tasks SET status = 'running' WHERE id = $task AND status IN ('queued', 'running');
COMMIT;                                                           -- 提交后发 export.changed

-- 2. 心跳（每 JOB_HEARTBEAT_SECS 秒；有进度时最多每秒一次，顺带写 progress_done）：刷新心跳，同时看任务是否已取消。
--    owned = false：已经被超时回收、别的执行器接手了，立即停下、什么都不再写。
WITH hb AS (
  UPDATE background_jobs SET heartbeat_at = now(), progress_done = COALESCE($progress, progress_done)
  WHERE id = $job AND status = 'running' AND runner_id = $w AND attempts = $n
  RETURNING id)
SELECT EXISTS (SELECT 1 FROM hb) AS owned,
       COALESCE((SELECT status = 'canceled' FROM export_tasks WHERE id = $task), false) AS cancel_requested;

-- 3. 执行期间不开事务：读冻结的页是一条语句（审核状态是执行时的当前值），之后从 S3 读页、生成、上传都不碰库。
SELECT x.archive_item_id, x.page_no, x.file_id, x.snapshot_id, s.version_no, s.content_sha256, s.file_size,
       s.mime_type, s.upload_name, COALESCE(ff.review_status, 'unassigned'), s.storage_profile_id, s.storage_key
FROM export_task_files x
JOIN file_snapshots s ON s.id = x.snapshot_id
LEFT JOIN folder_files ff ON ff.file_id = x.file_id
WHERE x.export_task_id = $task;
-- 开始生成之前记下这次的导出物写在哪：key 是 <path_prefix>exports/<project_code>/<task>/export-<attempts>.zip，
-- 每次执行一个新 key。旧 key 在核对租约之后、锁住任务行时才读，提交之后才删（删不掉只记日志，交给到期清理）：
-- 被回收的旧执行恢复后在第一句就对不上租约，不会删到接手的执行刚传完的导出物（审查第 1 轮）。
BEGIN;
SELECT status, runner_id, attempts FROM background_jobs WHERE id = $job FOR NO KEY UPDATE;   -- 核对租约，不对 → ROLLBACK
SELECT status, storage_profile_id, output_key FROM export_tasks WHERE id = $task FOR NO KEY UPDATE;  -- 不是 running → ROLLBACK
UPDATE export_tasks SET storage_profile_id = $profile, output_key = $key, output_size = NULL
WHERE id = $task AND status = 'running';
COMMIT;                                                           -- 之后删原来记着的那个（和这次的不一样时）

-- 4. 完成（导出物已经传完）：一个事务。先写 file_logs（20 万页要几秒，这时不锁 job 行和任务行，取消、心跳不用等），
--    再 job 行 → 任务行、看状态；失去租约、任务已取消 → 整个 ROLLBACK（file_logs 一起撤掉），删掉导出物，已取消的
--    再按 6 收尾（审查第 1 轮：原来先锁行再写 file_logs，20 万页时心跳、取消要等约 4.5 秒）。
BEGIN;
SELECT project_id, created_by, export_format FROM export_tasks WHERE id = $task;   -- 建好就不变，不加锁
-- 每页一条 file_logs export（一条 INSERT，按导出顺序；档号是冻结的，和 zip 里一致；操作人是建任务的人）。
-- 外键检查对 files 行拿 FOR KEY SHARE，和审核、挂接的 FOR NO KEY UPDATE 不冲突。
INSERT INTO file_logs (project_id, file_id, action, operator_id, detail)
SELECT $pid, x.file_id, 'export', $created_by,
       jsonb_build_object('export_task_id', $task, 'archive_no', i.archive_no, 'page_no', x.page_no,
                          'version_no', s.version_no, 'snapshot_id', x.snapshot_id, 'export_format', $format)
FROM unnest($item_ids::uuid[], $archive_nos::text[]) WITH ORDINALITY AS i(item_id, archive_no, seq)
JOIN export_task_files x ON x.export_task_id = $task AND x.archive_item_id = i.item_id
JOIN file_snapshots s ON s.id = x.snapshot_id
ORDER BY i.seq, x.page_no;
SELECT status, runner_id, attempts FROM background_jobs WHERE id = $job FOR NO KEY UPDATE;   -- 不是自己的 → ROLLBACK
SELECT t.status, p.settings FROM export_tasks t JOIN projects p ON p.id = t.project_id
WHERE t.id = $task FOR NO KEY UPDATE OF t;                                                   -- canceled → ROLLBACK
UPDATE export_tasks SET status = 'done', finished_at = now(), output_size = $size,
       expires_at = now() + make_interval(days => $retention_days)        -- 项目设置 export.retention_days
WHERE id = $task AND status = 'running';
UPDATE background_jobs SET status = 'done', finished_at = now(), progress_done = progress_total, error_message = NULL
WHERE id = $job;
COMMIT;                                                           -- 提交后发 export.changed

-- 5. 失败（已经中止上传、删掉临时目录）：job 行 → 任务行。任务已取消 → job canceled；能重试（读写 S3、临时盘出错）
--    而且 attempts < max_attempts → 排队，退避 JOB_RETRY_BACKOFF_SECS × 4^(attempts−1)，最多 1 小时；否则 failed。
BEGIN;
SELECT status, runner_id, attempts, max_attempts FROM background_jobs WHERE id = $job FOR NO KEY UPDATE;
SELECT status FROM export_tasks WHERE id = $task FOR NO KEY UPDATE;
UPDATE background_jobs SET status = 'queued', run_after = now() + make_interval(secs => $delay), runner_id = NULL,
       heartbeat_at = NULL, progress_done = 0, error_message = $message  -- "第 n 次执行失败，x 后自动重试：…"
WHERE id = $job;
UPDATE export_tasks SET status = 'queued' WHERE id = $task AND status = 'running';
-- 或者：
UPDATE background_jobs SET status = 'failed', finished_at = now(), error_message = $message WHERE id = $job;
UPDATE export_tasks SET status = 'failed', finished_at = now() WHERE id = $task AND status = 'running';
COMMIT;

-- 6. 取消后收尾（心跳看到任务已取消，停下、中止分片上传、删临时目录之后）：
UPDATE background_jobs SET status = 'canceled', finished_at = now() WHERE id = $job;   -- 先核对租约（同上）
-- 7. 优雅退出时放回队列（这次不算一次尝试，马上可以再领）；任务已取消的改成 canceled：
UPDATE background_jobs SET status = 'queued', attempts = GREATEST(attempts - 1, 0), run_after = now(),
       runner_id = NULL, heartbeat_at = NULL, progress_done = 0
WHERE id = $job;                                                                       -- 先核对租约（同上）
UPDATE export_tasks SET status = 'queued' WHERE id = $task AND status = 'running';

-- 8. 超时回收（每个执行器每个心跳间隔一次）：心跳超过 JOB_HEARTBEAT_TIMEOUT_SECS 的 running job，逐个短事务。
SELECT id FROM background_jobs
WHERE status = 'running' AND COALESCE(heartbeat_at, started_at, created_at) < now() - make_interval(secs => $timeout)
ORDER BY COALESCE(heartbeat_at, started_at, created_at) LIMIT 50;
BEGIN;
SELECT job_type, project_id, attempts, max_attempts, payload->>'export_task_id' FROM background_jobs
WHERE id = $job AND status = 'running'
  AND COALESCE(heartbeat_at, started_at, created_at) < now() - make_interval(secs => $timeout)
FOR NO KEY UPDATE SKIP LOCKED;                                    -- 正在收尾的跳过
-- 然后同 5：任务已取消 → canceled；attempts < max_attempts → 排队重试（"执行中断：超过 x 没有心跳"）；否则 failed。
-- 那次执行留下的导出物：下一次执行开始时按任务上记着的 key 删掉；不再执行的（failed、canceled）由到期清理删。
COMMIT;

-- 9. 到期清理（每 EXPORT_CLEANUP_INTERVAL_SECS 秒，启动时也做一次）。任务和明细保留。
--    (a) 记着的：done 而过期超过宽限期的（expires_at + 存储的 url_expire_hours + 24 小时：按天取整的下载地址最长能用到
--        这时候，下载接口在 expires_at 之后就拒绝），失败、取消留下的（job 已经不在 running）。存储已停用的不选；
--        按 (created_at, id) 分页走完全部，删不掉的跳过、下一轮再试。删掉对象（不存在也算成功）后清空 output_key。
SELECT t.id, t.project_id, t.status, t.storage_profile_id, t.output_key, t.created_at
FROM export_tasks t
JOIN storage_profiles sp ON sp.id = t.storage_profile_id AND sp.status <> 'disabled'
LEFT JOIN background_jobs j ON j.id = t.job_id
WHERE t.output_key IS NOT NULL
  AND ((t.status = 'done' AND t.expires_at + make_interval(hours => sp.url_expire_hours::int + 24) <= now())
       OR (t.status IN ('failed', 'canceled') AND (j.status IS NULL OR j.status <> 'running')))
  AND ($c_at IS NULL OR (t.created_at, t.id) > ($c_at, $c_id))
ORDER BY t.created_at, t.id LIMIT 100;
UPDATE export_tasks SET output_key = NULL,
       storage_profile_id = CASE WHEN status = 'done' THEN storage_profile_id END   -- done 的保留"存在哪"
WHERE id = $task AND output_key = $key;
--    (b) 没人记着的（审查第 1、2 轮）：最近 7 天里结束的任务（SWEEP_WINDOW；不按 attempts 筛，退出时放回队列会把它
--        减回去）。在每个没停用的存储上列出 <path_prefix>exports/<code>/<task>/，删掉 key 不是 output_key 的对象
--        （只比 key、不比存储：两条存储可能指向同一个桶和前缀）。结束超过 1 小时、删干净了的记在内存里，以后不再列，
--        出了窗口的从内存里删掉。finished_at 没有索引，是对 export_tasks 的顺序扫描。
SELECT t.id, p.project_code, t.status, t.storage_profile_id, t.output_key, t.finished_at, t.created_at
FROM export_tasks t JOIN projects p ON p.id = t.project_id LEFT JOIN background_jobs j ON j.id = t.job_id
WHERE t.status IN ('done', 'failed', 'canceled') AND (j.status IS NULL OR j.status <> 'running')
  AND t.finished_at > now() - interval '7 days'
  AND ($c_at IS NULL OR (t.created_at, t.id) > ($c_at, $c_id))
ORDER BY t.created_at, t.id LIMIT 100;
SELECT id, path_prefix FROM storage_profiles WHERE status <> 'disabled';

-- 10. 下载（GET /api/export-tasks/{id}/download）：只读，done、没过期、output_key 不为空才给预签名地址；写一条审计。
SELECT t.status, t.scope_type, p.project_code, t.storage_profile_id, t.output_key, t.output_size, t.finished_at,
       t.expires_at, (t.expires_at IS NOT NULL AND t.expires_at <= now()) AS expired,
       j.payload - 'items' AS header, j.payload->'items'->0->>'archive_no' AS first_archive_no
FROM export_tasks t JOIN projects p ON p.id = t.project_id LEFT JOIN background_jobs j ON j.id = t.job_id
WHERE t.id = $task AND t.project_id = $pid;
INSERT INTO audit_logs (project_id, operator_id, target_type, target_id, action, detail)
VALUES ($pid, $user, 'export_task', $task, 'download', $detail);  -- {file_name, size, storage_profile_id, output_key, url_expires_at}
