package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDatabaseConfigParsing(t *testing.T) {
	yamlContent := `
database:
  path: "./data/test.db"
`
	cfg, err := parseTestConfig(yamlContent)
	if err != nil {
		t.Fatalf("parse config failed: %v", err)
	}

	if cfg.Database.Path != "./data/test.db" {
		t.Errorf("expected path './data/test.db', got '%s'", cfg.Database.Path)
	}
}

func TestDatabaseConfigDefault(t *testing.T) {
	yamlContent := `
server:
  listen: ":8080"
`
	cfg, err := parseTestConfig(yamlContent)
	if err != nil {
		t.Fatalf("parse config failed: %v", err)
	}

	if cfg.Database.Path != "" {
		t.Errorf("expected empty default path, got '%s'", cfg.Database.Path)
	}
}

func parseTestConfig(yamlContent string) (*Config, error) {
	var cfg Config
	err := yaml.Unmarshal([]byte(yamlContent), &cfg)
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}