// Package audit 写 audit_logs：文件以外的变更记录（硬性规则 9，requirements 3.5）。各模块在自己的事务里调用。
//
// 格式：
//   - project_id：项目内的变更填所属项目；全局的变更（用户、角色、存储配置）为空；
//   - operator_id：当前用户；为空表示系统（例如初始管理员）；
//   - target_type、target_id：改的是什么（user、role、storage_profile、project、folder……）；
//   - action：create、update、delete，或具体的动作（disable、set_roles……）；
//   - detail：`{"before": {…}, "after": {…}}`，只放这次变了的字段。**不放密码、密码哈希、密钥和它的密文。**
//
// 空修改不写库、不审计；登录、退出不审计。
package audit

import (
	"context"

	"github.com/google/uuid"

	"doclite/server/internal/platform/db"
)

// target_type 的取值。
const (
	TargetUser           = "user"
	TargetRole           = "role"
	TargetProject        = "project"
	TargetStorageProfile = "storage_profile"
	// M4-1：归类节点、档案条目。
	TargetArchiveCategory = "archive_category"
	TargetArchiveItem     = "archive_item"
	TargetFolder          = "folder"
	// M5-2：导出任务（create、cancel、download）。
	TargetExportTask = "export_task"
)

// Entry 是一条审计记录。
type Entry struct {
	ProjectID  *uuid.UUID
	OperatorID *uuid.UUID
	TargetType string
	TargetID   uuid.UUID
	Action     string
	Detail     any
}

// Write 在调用方的事务里写一条审计记录。
func Write(ctx context.Context, q db.Querier, e Entry) error {
	detail := e.Detail
	if detail == nil {
		detail = map[string]any{}
	}
	_, err := q.Exec(ctx, `INSERT INTO audit_logs (project_id, operator_id, target_type, target_id, action, detail)
VALUES ($1, $2, $3, $4, $5, $6)`, e.ProjectID, e.OperatorID, e.TargetType, e.TargetID, e.Action, detail)
	return err
}

// Operator 把操作人 id 转成 Entry.OperatorID 需要的指针。
func Operator(id uuid.UUID) *uuid.UUID { return &id }
