package runtimeconfig

import (
	"testing"

	"github.com/Marstheway/oh-my-api/internal/config"
)

func TestManagerReorderModelGroups(t *testing.T) {
	cfg, configPath := createTestConfig(t)
	mgr, err := NewManager(cfg, configPath, &mockRebuilder{}, &mockReinit{})
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	groups := mgr.ListModelGroups()
	if len(groups) != 2 || groups[0].Name != "gpt-4o" || groups[1].Name != "unused" {
		t.Fatalf("unexpected initial order: %+v", namesOf(groups))
	}

	if err := mgr.ReorderModelGroups([]string{"unused", "gpt-4o"}); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	groups = mgr.ListModelGroups()
	if groups[0].Name != "unused" || groups[1].Name != "gpt-4o" {
		t.Fatalf("want unused,gpt-4o got %v", namesOf(groups))
	}

	err = mgr.ReorderModelGroups([]string{"unused"})
	if err == nil {
		t.Fatal("expected error for incomplete permutation")
	}
	rerr, ok := err.(*Error)
	if !ok || rerr.Code != ErrCodeBadRequest {
		t.Fatalf("want bad_request, got %T %v", err, err)
	}

	err = mgr.ReorderModelGroups([]string{"unused", "unused"})
	if err == nil {
		t.Fatal("expected error for duplicate")
	}

	err = mgr.ReorderModelGroups([]string{"unused", "nope"})
	if err == nil {
		t.Fatal("expected error for unknown")
	}

	// 失败后顺序不变
	groups = mgr.ListModelGroups()
	if groups[0].Name != "unused" || groups[1].Name != "gpt-4o" {
		t.Fatalf("order mutated after failed reorder: %v", namesOf(groups))
	}
}

func TestManagerReorderRedirects(t *testing.T) {
	cfg, configPath := createTestConfig(t)
	mgr, err := NewManager(cfg, configPath, &mockRebuilder{}, &mockReinit{})
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	if err := mgr.CreateRedirect(&RedirectInput{Source: "alias-b", Target: "gpt-4o"}); err != nil {
		t.Fatalf("create redirect: %v", err)
	}

	list := mgr.ListRedirects()
	if len(list) != 2 {
		t.Fatalf("want 2 redirects, got %d", len(list))
	}
	// 初始 yaml 一条 gpt-4，再 append alias-b
	if list[0].Source != "gpt-4" || list[1].Source != "alias-b" {
		t.Fatalf("unexpected initial redirect order: %s, %s", list[0].Source, list[1].Source)
	}

	if err := mgr.ReorderRedirects([]string{"alias-b", "gpt-4"}); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	list = mgr.ListRedirects()
	if list[0].Source != "alias-b" || list[1].Source != "gpt-4" {
		t.Fatalf("want alias-b,gpt-4 got %s,%s", list[0].Source, list[1].Source)
	}

	if err := mgr.ReorderRedirects(nil); err == nil {
		t.Fatal("expected error for nil sources")
	}
}

func namesOf(groups []config.ModelGroupConfig) []string {
	out := make([]string, len(groups))
	for i, g := range groups {
		out[i] = g.Name
	}
	return out
}
