package migrate

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

//go:embed all:migrations
var migrations embed.FS

// Run 执行数据库迁移
func Run(dbPath string) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	goose.SetDialect("sqlite3")

	// 设置嵌入的文件系统，使用 "." 表示当前目录（即 migrations 子目录）
	migrationFS, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("get migration subdirectory: %w", err)
	}
	goose.SetBaseFS(migrationFS)

	if err := goose.Up(db, "."); err != nil {
		return fmt.Errorf("migration failed: %w", err)
	}

	return nil
}
