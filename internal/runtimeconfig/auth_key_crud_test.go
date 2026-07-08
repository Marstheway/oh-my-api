package runtimeconfig

import (
	"testing"

	"github.com/Marstheway/oh-my-api/internal/config"
)

func newAuthKeyDraft() *config.Config {
	return &config.Config{
		Inbound: config.InboundConfig{
			Auth: config.AuthConfig{
				Keys: []config.KeyConfig{
					{Name: "key1", Key: "sk-aaa"},
					{Name: "key2", Key: "sk-bbb"},
				},
			},
		},
	}
}

func TestAuthKeyCRUD_Create(t *testing.T) {
	t.Run("normal create", func(t *testing.T) {
		draft := newAuthKeyDraft()
		crud := NewAuthKeyCRUD(draft)
		err := crud.Create(&AuthKeyInput{Name: "key3", Key: "sk-ccc"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(draft.Inbound.Auth.Keys) != 3 {
			t.Errorf("keys len = %d, want 3", len(draft.Inbound.Auth.Keys))
		}
		got, ok := crud.Get("key3")
		if !ok {
			t.Fatal("created key not found")
		}
		if got.Key != "sk-ccc" {
			t.Errorf("key = %q, want %q", got.Key, "sk-ccc")
		}
	})

	t.Run("empty name", func(t *testing.T) {
		crud := NewAuthKeyCRUD(newAuthKeyDraft())
		err := crud.Create(&AuthKeyInput{Name: "  ", Key: "sk-x"})
		if err == nil {
			t.Fatal("expected error for empty name")
		}
		if e, ok := err.(*Error); !ok || e.Code != ErrCodeBadRequest {
			t.Errorf("error code = %v, want %v", err, ErrCodeBadRequest)
		}
	})

	t.Run("empty key", func(t *testing.T) {
		crud := NewAuthKeyCRUD(newAuthKeyDraft())
		err := crud.Create(&AuthKeyInput{Name: "key3", Key: ""})
		if err == nil {
			t.Fatal("expected error for empty key")
		}
		if e, ok := err.(*Error); !ok || e.Code != ErrCodeBadRequest {
			t.Errorf("error code = %v, want %v", err, ErrCodeBadRequest)
		}
	})

	t.Run("duplicate name", func(t *testing.T) {
		crud := NewAuthKeyCRUD(newAuthKeyDraft())
		err := crud.Create(&AuthKeyInput{Name: "key1", Key: "sk-x"})
		if err == nil {
			t.Fatal("expected error for duplicate name")
		}
		if e, ok := err.(*Error); !ok || e.Code != ErrCodeConflict {
			t.Errorf("error code = %v, want %v", err, ErrCodeConflict)
		}
	})
}

func TestAuthKeyCRUD_Update(t *testing.T) {
	t.Run("update key value", func(t *testing.T) {
		draft := newAuthKeyDraft()
		crud := NewAuthKeyCRUD(draft)
		err := crud.Update("key1", &AuthKeyInput{Name: "key1", Key: "sk-updated"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got, ok := crud.Get("key1")
		if !ok {
			t.Fatal("key not found after update")
		}
		if got.Key != "sk-updated" {
			t.Errorf("key = %q, want %q", got.Key, "sk-updated")
		}
	})

	t.Run("rename", func(t *testing.T) {
		draft := newAuthKeyDraft()
		crud := NewAuthKeyCRUD(draft)
		err := crud.Update("key1", &AuthKeyInput{Name: "key1-renamed", Key: "sk-aaa"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, ok := crud.Get("key1"); ok {
			t.Error("old name should not exist after rename")
		}
		if _, ok := crud.Get("key1-renamed"); !ok {
			t.Error("new name should exist after rename")
		}
	})

	t.Run("not found", func(t *testing.T) {
		crud := NewAuthKeyCRUD(newAuthKeyDraft())
		err := crud.Update("nope", &AuthKeyInput{Name: "nope", Key: "sk-x"})
		if err == nil {
			t.Fatal("expected error for not found")
		}
		if e, ok := err.(*Error); !ok || e.Code != ErrCodeNotFound {
			t.Errorf("error code = %v, want %v", err, ErrCodeNotFound)
		}
	})

	t.Run("rename to existing", func(t *testing.T) {
		crud := NewAuthKeyCRUD(newAuthKeyDraft())
		err := crud.Update("key1", &AuthKeyInput{Name: "key2", Key: "sk-x"})
		if err == nil {
			t.Fatal("expected error for rename to existing")
		}
		if e, ok := err.(*Error); !ok || e.Code != ErrCodeConflict {
			t.Errorf("error code = %v, want %v", err, ErrCodeConflict)
		}
	})

	t.Run("empty name", func(t *testing.T) {
		crud := NewAuthKeyCRUD(newAuthKeyDraft())
		err := crud.Update("key1", &AuthKeyInput{Name: "", Key: "sk-x"})
		if err == nil {
			t.Fatal("expected error for empty name")
		}
		if e, ok := err.(*Error); !ok || e.Code != ErrCodeBadRequest {
			t.Errorf("error code = %v, want %v", err, ErrCodeBadRequest)
		}
	})

	t.Run("empty key", func(t *testing.T) {
		crud := NewAuthKeyCRUD(newAuthKeyDraft())
		err := crud.Update("key1", &AuthKeyInput{Name: "key1", Key: ""})
		if err == nil {
			t.Fatal("expected error for empty key")
		}
		if e, ok := err.(*Error); !ok || e.Code != ErrCodeBadRequest {
			t.Errorf("error code = %v, want %v", err, ErrCodeBadRequest)
		}
	})
}

func TestAuthKeyCRUD_Delete(t *testing.T) {
	t.Run("normal delete", func(t *testing.T) {
		draft := newAuthKeyDraft()
		crud := NewAuthKeyCRUD(draft)
		err := crud.Delete("key1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(draft.Inbound.Auth.Keys) != 1 {
			t.Errorf("keys len = %d, want 1", len(draft.Inbound.Auth.Keys))
		}
		if _, ok := crud.Get("key1"); ok {
			t.Error("deleted key should not exist")
		}
	})

	t.Run("not found", func(t *testing.T) {
		crud := NewAuthKeyCRUD(newAuthKeyDraft())
		err := crud.Delete("nope")
		if err == nil {
			t.Fatal("expected error for not found")
		}
		if e, ok := err.(*Error); !ok || e.Code != ErrCodeNotFound {
			t.Errorf("error code = %v, want %v", err, ErrCodeNotFound)
		}
	})
}

func TestAuthKeyCRUD_Get(t *testing.T) {
	crud := NewAuthKeyCRUD(newAuthKeyDraft())

	t.Run("exists", func(t *testing.T) {
		got, ok := crud.Get("key1")
		if !ok {
			t.Fatal("expected to find key1")
		}
		if got.Key != "sk-aaa" {
			t.Errorf("key = %q, want %q", got.Key, "sk-aaa")
		}
	})

	t.Run("not exists", func(t *testing.T) {
		_, ok := crud.Get("nope")
		if ok {
			t.Error("should not find non-existent key")
		}
	})
}

func TestAuthKeyCRUD_List(t *testing.T) {
	crud := NewAuthKeyCRUD(newAuthKeyDraft())
	list := crud.List()
	if len(list) != 2 {
		t.Fatalf("list len = %d, want 2", len(list))
	}
	// 验证返回完整 key（不脱敏）
	for _, k := range list {
		if k.Key == "" {
			t.Errorf("key %q has empty key value (should not be masked)", k.Name)
		}
	}
}
