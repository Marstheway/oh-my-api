package stats

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// 每个测试前重置全局状态，确保测试隔离
	Reset()
	code := m.Run()
	// 测试后清理
	Reset()
	// 清理测试数据库文件
	os.Remove("./data/oh-my-api.db")
	os.Remove("./data/oh-my-api.db-wal")
	os.Remove("./data/oh-my-api.db-shm")
	os.Exit(code)
}

// TestDefaultValues 测试初始状态（需要在测试中手动重置后检查）
func TestDefaultValues(ot *testing.T) {
	// 先重置确保干净状态
	Reset()

	// recorder 和 querier 初始应为 nil
	if recorder != nil {
		ot.Error("expected recorder to be nil before Init")
	}
	if querier != nil {
		ot.Error("expected querier to be nil before Init")
	}
}