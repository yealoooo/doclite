package project

import (
	"context"
	"hash/fnv"

	"github.com/google/uuid"

	"doclite/server/internal/platform/db"
)

// 项目的两把树锁（requirements 3.6），用 PostgreSQL 的事务级咨询锁实现：事务结束自动释放，死锁由 PG 检测。
//
//   - 文件树锁（每项目一把）：共享 = 建或改名文件夹、上传、复制、移动文件、重扫、删单个文件、审核写、改项目；
//     排他 = 移动文件夹、删子树、删项目；
//   - 归类树锁（每项目一把）：共享 = 建或改名节点、条目增改移删和状态、挂接、项目改名；
//     排他 = 移动或删节点子树、改 category_code/type、批量重算档号。
//   - 顺序：归类树锁 → 节点行 → 条目行（id 升序）→ 文件树锁 → 项目行 → files 行（id 升序）→ folders 行
//     → 存储配置行 → 内容锁。审核不拿归类锁；两棵树都要的操作先拿归类锁和归类行，最后拿文件树锁。例外：复制在内容锁
//     之后可能再对存储配置行拿 FOR KEY SHARE（见 LockContent）。存储迁移改位置的事务是 job 行 → 存储配置行 → 内容锁。
//
// 咨询锁的键是 (类别, 项目 id 的 32 位哈希)：不同项目哈希相同只会多等一会儿，不影响正确性。
// 咨询锁类别的登记（新加的类别号写在这里，不要重复）：
//   - 6000：迁移执行器（6000,1，会话级）、测试的模板库（6000,2）；
//   - 6001、6002：这里的文件树锁、归类树锁（事务级）；
//   - 6003：内容锁（这里的 LockContent，file.LockContent 是它；事务级）：上传、重扫、复制（锁内重读源快照）、
//     存储迁移改快照位置（storage 的迁移任务）都拿它；
//   - 6004：上一版的任务领取锁，这一版不用，留空；
//   - 6005：档号（catalog 的 lockArchiveNo，事务级）；
//   - 6006：后台任务执行器的单实例锁（6006,0，会话级，jobs.Executor 在一条专用连接上持有）；
//   - 6011：许可证席位检查（auth 的 lockClassSeats，6011,0，事务级，登录时在锁住用户行之后取）。
const (
	lockClassFileTree    = 6001
	lockClassCatalogTree = 6002
)

func lockKey(projectID uuid.UUID) int32 {
	h := fnv.New32a()
	h.Write(projectID[:])
	return int32(h.Sum32())
}

func advisoryLock(ctx context.Context, tx db.Querier, class int32, projectID uuid.UUID, exclusive bool) error {
	fn := "pg_advisory_xact_lock_shared"
	if exclusive {
		fn = "pg_advisory_xact_lock"
	}
	_, err := tx.Exec(ctx, `SELECT `+fn+`($1::int4, $2::int4)`, class, lockKey(projectID))
	return err
}

// LockFileTree 在事务 tx 里拿项目的文件树锁（exclusive 为 false 是共享锁），到事务结束为止。
func LockFileTree(ctx context.Context, tx db.Querier, projectID uuid.UUID, exclusive bool) error {
	return advisoryLock(ctx, tx, lockClassFileTree, projectID, exclusive)
}

// LockCatalogTree 在事务 tx 里拿项目的归类树锁（exclusive 为 false 是共享锁），到事务结束为止。
func LockCatalogTree(ctx context.Context, tx db.Querier, projectID uuid.UUID, exclusive bool) error {
	return advisoryLock(ctx, tx, lockClassCatalogTree, projectID, exclusive)
}

// lockClassContent 是内容锁的咨询锁类别（见上面的登记）。
const lockClassContent = 6003

// contentKey 是 (项目, sha256) 的内容锁的键。
func contentKey(projectID uuid.UUID, sha []byte) int32 {
	h := fnv.New32a()
	h.Write(projectID[:])
	h.Write(sha)
	return int32(h.Sum32())
}

// LockContent 在事务 tx 里拿 (项目, sha256) 的内容锁（事务级咨询锁，提交或回滚时释放）。它是事务里最后拿的锁（在存储
// 配置行之后），拿到之后在锁内再查一次重、再读一次要复用的快照（requirements 3.6）。唯一的例外：复制（folder.CopyEntry）在内容锁里重读源快照，发现
// 它刚被存储迁移改到了别的存储时，在内容锁之后再对那个存储配置行拿 FOR KEY SHARE（和插入快照时外键检查拿的锁一样）。
// 不会成环：对存储配置行拿冲突锁（FOR UPDATE）的只有设默认、停用、删除三处，它们都不拿、也不等内容锁、树锁、files 行、
// folders 行；其余拿存储配置行的都是 FOR KEY SHARE / FOR NO KEY UPDATE，和 FOR KEY SHARE 不冲突。
// 不同的 (项目, sha) 撞到同一个键只是多排一会儿队。
func LockContent(ctx context.Context, tx db.Querier, projectID uuid.UUID, sha []byte) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1::int4, $2::int4)`, lockClassContent, contentKey(projectID, sha))
	return err
}

// LockContents 拿同一个 sha256 在几个项目里的内容锁：按锁的键升序、去重，一条语句拿完（两个事务各拿几把时不会互相等成
// 死锁）。存储迁移用：一个对象的快照一般只在一个项目里。
func LockContents(ctx context.Context, tx db.Querier, projectIDs []uuid.UUID, sha []byte) error {
	keys := make([]int32, 0, len(projectIDs))
	for _, p := range projectIDs {
		keys = append(keys, contentKey(p, sha))
	}
	return db.AdvisoryXactLocks(ctx, tx, lockClassContent, keys)
}
