package config

import (
	"path/filepath"
)

// CatalogPath 根据配置返回 catalog_upstream.json 的绝对路径。
// probe 写入端和 handler 读取端共同使用此规则，保证两端操作同一文件。
func CatalogPath(cfg *Config) string {
	if cfg != nil && cfg.Database.Path != "" {
		return filepath.Join(filepath.Dir(cfg.Database.Path), "catalog_upstream.json")
	}
	return filepath.Join(".", "data", "catalog_upstream.json")
}

// ModelsDevPath 根据配置返回 models_dev_ctx.tsv 的绝对路径（与 catalog_upstream.json 同目录）。
// 该文件是 models.dev 社区目录的紧凑索引（id → context_length），只存小写模型 id 与上下文窗口。
func ModelsDevPath(cfg *Config) string {
	if cfg != nil && cfg.Database.Path != "" {
		return filepath.Join(filepath.Dir(cfg.Database.Path), "models_dev_ctx.tsv")
	}
	return filepath.Join(".", "data", "models_dev_ctx.tsv")
}
