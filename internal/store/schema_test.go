package store

import (
	"strings"
	"testing"
)

// 列清单、写入值、Scan 目标必须始终对齐；加列时漏改任何一处都在这里失败。
func TestLogColumnDefinitionsStayInSync(t *testing.T) {
	var e LogEntry
	var n logNulls
	targets := logTargets(&e, &n)
	for _, c := range logColumns {
		if targets[c] == nil {
			t.Errorf("column %q has no scan target", c)
		}
	}
	if len(targets) != len(logColumns) {
		t.Errorf("targets=%d columns=%d", len(targets), len(logColumns))
	}
	if got := len(e.logValues()); got != len(logColumns)-1 {
		t.Errorf("logValues=%d, want %d (all columns except id)", got, len(logColumns)-1)
	}
	if cols := visibleLogColumns(false); len(cols) != len(logColumns)-len(logBodyColumns) {
		t.Errorf("list columns=%d, want %d", len(cols), len(logColumns)-len(logBodyColumns))
	}
}

// 补列清单是静态声明，必须与建表 DDL 逐字一致：新增列只改了 schema、
// 或改了类型却忘了同步 addedColumns，都会在这里失败。
func TestAddedColumnsMatchSchema(t *testing.T) {
	for _, c := range addedColumns {
		declared := c.column + " " + c.ddl
		if !strings.Contains(schema, declared) {
			t.Errorf("added column %s.%s: %q not declared verbatim in schema", c.table, c.column, declared)
		}
	}
}
