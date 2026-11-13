package store

import (
	"errors"
	"fmt"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var (
	// ErrNotFound 更新对象不存在（由 RowsAffected=0 判定）。
	ErrNotFound = errors.New("store: not found")
	// ErrConflict 唯一约束冲突，例如同渠道下模型重名、标准名重名。
	ErrConflict = errors.New("store: conflict")
	// ErrInvalidReference 外键引用的对象不存在，例如绑定了不存在的模型或标准名。
	ErrInvalidReference = errors.New("store: invalid reference")
)

// classifyConstraintErr 把驱动的约束错误码翻译成领域错误，向上层提供稳定的
// errors.Is 契约。不再嗅探错误文本：modernc sqlite 会返回带 Code() 的 *Error。
func classifyConstraintErr(err error) error {
	if err == nil {
		return nil
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		switch sqliteErr.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
			return fmt.Errorf("%w: %v", ErrConflict, err)
		case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY:
			return fmt.Errorf("%w: %v", ErrInvalidReference, err)
		}
	}
	return err
}
