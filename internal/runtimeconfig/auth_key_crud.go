package runtimeconfig

import (
	"strings"

	"github.com/Marstheway/oh-my-api/internal/config"
)

// AuthKeyCRUD 实现 auth key 的 CRUD 规则检查
type AuthKeyCRUD struct {
	draft *config.Config
}

func NewAuthKeyCRUD(draft *config.Config) *AuthKeyCRUD {
	return &AuthKeyCRUD{draft: draft}
}

// Create 执行创建操作
func (c *AuthKeyCRUD) Create(input *AuthKeyInput) error {
	if err := c.validateCreate(input); err != nil {
		return err
	}

	c.draft.Inbound.Auth.Keys = append(c.draft.Inbound.Auth.Keys, config.KeyConfig{
		Name: input.Name,
		Key:  input.Key,
	})
	return nil
}

func (c *AuthKeyCRUD) validateCreate(input *AuthKeyInput) *Error {
	if strings.TrimSpace(input.Name) == "" {
		return &Error{Code: ErrCodeBadRequest, Field: "name", Message: "must not be empty"}
	}
	if strings.TrimSpace(input.Key) == "" {
		return &Error{Code: ErrCodeBadRequest, Field: "key", Message: "must not be empty"}
	}
	if c.keyExists(input.Name) {
		return &Error{Code: ErrCodeConflict, Field: "name", Message: "already exists"}
	}
	return nil
}

// Update 执行更新操作（允许改名）
func (c *AuthKeyCRUD) Update(oldName string, input *AuthKeyInput) error {
	if err := c.validateUpdate(oldName, input); err != nil {
		return err
	}

	for i, k := range c.draft.Inbound.Auth.Keys {
		if k.Name == oldName {
			c.draft.Inbound.Auth.Keys[i] = config.KeyConfig{
				Name: input.Name,
				Key:  input.Key,
			}
			break
		}
	}
	return nil
}

func (c *AuthKeyCRUD) validateUpdate(oldName string, input *AuthKeyInput) *Error {
	if !c.keyExists(oldName) {
		return &Error{Code: ErrCodeNotFound, Field: "name", Message: "auth key not found"}
	}
	if strings.TrimSpace(input.Name) == "" {
		return &Error{Code: ErrCodeBadRequest, Field: "name", Message: "must not be empty"}
	}
	if strings.TrimSpace(input.Key) == "" {
		return &Error{Code: ErrCodeBadRequest, Field: "key", Message: "must not be empty"}
	}
	if input.Name != oldName && c.keyExists(input.Name) {
		return &Error{Code: ErrCodeConflict, Field: "name", Message: "already exists"}
	}
	return nil
}

// Delete 执行删除操作
func (c *AuthKeyCRUD) Delete(name string) error {
	if !c.keyExists(name) {
		return &Error{Code: ErrCodeNotFound, Field: "name", Message: "auth key not found"}
	}

	for i, k := range c.draft.Inbound.Auth.Keys {
		if k.Name == name {
			c.draft.Inbound.Auth.Keys = append(
				c.draft.Inbound.Auth.Keys[:i],
				c.draft.Inbound.Auth.Keys[i+1:]...,
			)
			break
		}
	}
	return nil
}

// Get 获取单个 auth key（返回完整 key）
func (c *AuthKeyCRUD) Get(name string) (AuthKeyOutput, bool) {
	for _, k := range c.draft.Inbound.Auth.Keys {
		if k.Name == name {
			return AuthKeyOutput{Name: k.Name, Key: k.Key}, true
		}
	}
	return AuthKeyOutput{}, false
}

// List 获取所有 auth keys（返回完整 key）
func (c *AuthKeyCRUD) List() []AuthKeyOutput {
	result := make([]AuthKeyOutput, 0, len(c.draft.Inbound.Auth.Keys))
	for _, k := range c.draft.Inbound.Auth.Keys {
		result = append(result, AuthKeyOutput{Name: k.Name, Key: k.Key})
	}
	return result
}

func (c *AuthKeyCRUD) keyExists(name string) bool {
	for _, k := range c.draft.Inbound.Auth.Keys {
		if k.Name == name {
			return true
		}
	}
	return false
}
