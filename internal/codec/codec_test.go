package codec

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

type testCodec struct {
	format Format
}

func (t testCodec) Format() Format {
	return t.format
}

func (t testCodec) DecodeRequest(*gin.Context) (any, error) {
	return nil, nil
}

func (t testCodec) EncodeRequest(Format, any, string) ([]byte, error) {
	return nil, nil
}

func (t testCodec) WriteResponse(*gin.Context, Format, *http.Response, bool, TokenCounter) error {
	return nil
}

func TestGetCodec_OpenAIChat(t *testing.T) {
	original := registry
	registry = map[Format]Codec{}
	t.Cleanup(func() {
		registry = original
	})

	register(FormatOpenAIChat, testCodec{format: FormatOpenAIChat})
	got, err := Get(FormatOpenAIChat)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.Format() != FormatOpenAIChat {
		t.Fatalf("Get format = %q, want %q", got.Format(), FormatOpenAIChat)
	}
}

func TestGetCodec_AnthropicMessages(t *testing.T) {
	original := registry
	registry = map[Format]Codec{}
	t.Cleanup(func() {
		registry = original
	})

	register(FormatAnthropicMessages, testCodec{format: FormatAnthropicMessages})
	got, err := Get(FormatAnthropicMessages)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.Format() != FormatAnthropicMessages {
		t.Fatalf("Get format = %q, want %q", got.Format(), FormatAnthropicMessages)
	}
}

func TestGetCodec_UnknownFormat(t *testing.T) {
	original := registry
	registry = map[Format]Codec{}
	t.Cleanup(func() {
		registry = original
	})

	if _, err := Get(Format("unknown")); err == nil {
		t.Fatalf("expected error for unknown codec")
	}
}

func TestNormalizeProviderFormat(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    Format
		wantErr bool
	}{
		{name: "openai", input: "openai", want: FormatOpenAIChat},
		{name: "anthropic", input: "anthropic", want: FormatAnthropicMessages},
		{name: "openai format", input: "openai.chat", want: FormatOpenAIChat},
		{name: "anthropic format", input: "anthropic.messages", want: FormatAnthropicMessages},
		{name: "unknown", input: "unknown", wantErr: true},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeProviderFormat(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeProviderFormat returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeProviderFormat(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSelectFormatForInbound(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		inbound Format
		want    Format
		wantErr bool
	}{
		{name: "single protocol", input: "openai", inbound: FormatOpenAIChat, want: FormatOpenAIChat},
		{name: "dual protocol prefer inbound", input: "openai/anthropic", inbound: FormatAnthropicMessages, want: FormatAnthropicMessages},
		{name: "dual protocol fallback first", input: "openai/anthropic", inbound: Format("other"), want: FormatOpenAIChat},
		{name: "invalid protocol", input: "openai/unknown", inbound: FormatOpenAIChat, wantErr: true},
		{name: "empty protocol", input: "", inbound: FormatOpenAIChat, wantErr: true},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SelectFormatForInbound(tt.input, tt.inbound)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("SelectFormatForInbound returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("SelectFormatForInbound(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFormatConstants(t *testing.T) {
	if FormatOpenAIChat != "openai.chat" {
		t.Fatalf("FormatOpenAIChat = %q, want %q", FormatOpenAIChat, "openai.chat")
	}
	if FormatAnthropicMessages != "anthropic.messages" {
		t.Fatalf("FormatAnthropicMessages = %q, want %q", FormatAnthropicMessages, "anthropic.messages")
	}
}

func TestFormatConstants_OpenAIResponse(t *testing.T) {
	if FormatOpenAIResponse != "openai.response" {
		t.Fatalf("FormatOpenAIResponse = %q, want %q", FormatOpenAIResponse, "openai.response")
	}
}

func TestNormalizeProviderFormat_OpenAIResponse(t *testing.T) {
	got, err := NormalizeProviderFormat("openai.response")
	if err != nil {
		t.Fatalf("NormalizeProviderFormat(%q) returned error: %v", "openai.response", err)
	}
	if got != FormatOpenAIResponse {
		t.Fatalf("NormalizeProviderFormat(%q) = %q, want %q", "openai.response", got, FormatOpenAIResponse)
	}
}

func TestSelectFormatForInbound_PreferExactResponse(t *testing.T) {
	got, err := SelectFormatForInbound("openai.response/openai", FormatOpenAIResponse)
	if err != nil {
		t.Fatalf("SelectFormatForInbound returned error: %v", err)
	}
	if got != FormatOpenAIResponse {
		t.Fatalf("SelectFormatForInbound = %q, want %q", got, FormatOpenAIResponse)
	}
}

func TestSelectFormatForInbound_FallbackWithoutResponse(t *testing.T) {
	got, err := SelectFormatForInbound("openai/anthropic", FormatOpenAIResponse)
	if err != nil {
		t.Fatalf("SelectFormatForInbound returned error: %v", err)
	}
	if got != FormatOpenAIChat {
		t.Fatalf("SelectFormatForInbound = %q, want %q", got, FormatOpenAIChat)
	}
}

func TestRegisterRejectsInvalidInput(t *testing.T) {
	t.Run("empty format", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatalf("expected panic for empty format")
			}
		}()
		register("", testCodec{format: FormatOpenAIChat})
	})

	t.Run("nil codec", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatalf("expected panic for nil codec")
			}
		}()
		register(FormatOpenAIChat, nil)
	})
}

func TestGetCodec_OpenAIResponse(t *testing.T) {
	original := registry
	registry = map[Format]Codec{}
	t.Cleanup(func() {
		registry = original
	})

	register(FormatOpenAIResponse, testCodec{format: FormatOpenAIResponse})
	got, err := Get(FormatOpenAIResponse)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.Format() != FormatOpenAIResponse {
		t.Fatalf("Get format = %q, want %q", got.Format(), FormatOpenAIResponse)
	}
}
