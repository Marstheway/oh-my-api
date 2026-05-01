package adaptor

import (
	"testing"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

func TestExtractUsageOpenAI(t *testing.T) {
	resp := &dto.ChatCompletionResponse{
		Usage: dto.Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
		},
	}

	usage := ExtractUsage(resp)
	if usage == nil {
		t.Fatal("expected non-nil usage")
	}
	if usage.InputTokens != 100 {
		t.Errorf("expected InputTokens 100, got %d", usage.InputTokens)
	}
	if usage.OutputTokens != 50 {
		t.Errorf("expected OutputTokens 50, got %d", usage.OutputTokens)
	}
}

func TestExtractUsageAnthropic(t *testing.T) {
	resp := &dto.ClaudeResponse{
		Usage: dto.ClaudeUsage{
			InputTokens:  200,
			OutputTokens: 100,
		},
	}

	usage := ExtractUsage(resp)
	if usage == nil {
		t.Fatal("expected non-nil usage")
	}
	if usage.InputTokens != 200 {
		t.Errorf("expected InputTokens 200, got %d", usage.InputTokens)
	}
	if usage.OutputTokens != 100 {
		t.Errorf("expected OutputTokens 100, got %d", usage.OutputTokens)
	}
}

func TestExtractUsageNil(t *testing.T) {
	usage := ExtractUsage(nil)
	if usage != nil {
		t.Errorf("expected nil for nil input, got %+v", usage)
	}
}

func TestExtractUsageUnknown(t *testing.T) {
	usage := ExtractUsage("invalid")
	if usage != nil {
		t.Errorf("expected nil for unknown type, got %+v", usage)
	}
}