package store

import (
	"context"
	"database/sql"
)

// replaceBindings 全量替换 model_mapping_models 中某一侧的全部绑定。
// ownerColumn/otherColumn 必须是本包内的列名常量，不接受外部输入；
// 重复 id 去重后插入，约束错误统一翻译为领域错误。
func replaceBindings(ctx context.Context, tx *sql.Tx, ownerColumn string, ownerID int64, otherColumn string, ids []int64) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM model_mapping_models WHERE `+ownerColumn+`=?`, ownerID); err != nil {
		return err
	}
	insert := `INSERT INTO model_mapping_models (` + ownerColumn + `, ` + otherColumn + `) VALUES (?, ?)`
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if _, err := tx.ExecContext(ctx, insert, ownerID, id); err != nil {
			return classifyConstraintErr(err)
		}
	}
	return nil
}
