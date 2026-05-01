package errors

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestWriteError_OpenAI(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		code       ErrorCode
		message    string
	}{
		{"unauthorized", http.StatusUnauthorized, ErrInvalidAPIKey, "invalid api key"},
		{"bad request", http.StatusBadRequest, ErrInvalidRequest, "bad request"},
		{"not found", http.StatusNotFound, ErrModelNotFound, "model not found"},
		{"gateway error", http.StatusBadGateway, ErrUpstreamError, "upstream error"},
		{"gateway timeout", http.StatusGatewayTimeout, ErrUpstreamTimeout, "upstream timeout"},
		{"internal", http.StatusInternalServerError, ErrInternal, "internal error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

			WriteError(c, ProtocolOpenAI, tt.statusCode, tt.code, tt.message)

			if w.Code != tt.statusCode {
				t.Errorf("status = %d, want %d", w.Code, tt.statusCode)
			}

			var result map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatalf("failed to parse response: %v", err)
			}

			errObj, ok := result["error"].(map[string]any)
			if !ok {
				t.Fatal("expected 'error' key in response")
			}

			if errObj["code"] != string(tt.code) {
				t.Errorf("code = %v, want %v", errObj["code"], tt.code)
			}
			if errObj["message"] != tt.message {
				t.Errorf("message = %v, want %v", errObj["message"], tt.message)
			}
		})
	}
}

func TestWriteError_Anthropic(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		code       ErrorCode
		message    string
		wantType   string
	}{
		{"unauthorized", http.StatusUnauthorized, ErrInvalidAPIKey, "invalid api key", "authentication_error"},
		{"not found", http.StatusNotFound, ErrModelNotFound, "model not found", "not_found_error"},
		{"bad request", http.StatusBadRequest, ErrInvalidRequest, "bad request", "invalid_request_error"},
		{"upstream error", http.StatusBadGateway, ErrUpstreamError, "upstream error", "api_error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/v1/messages", nil)

			WriteError(c, ProtocolAnthropic, tt.statusCode, tt.code, tt.message)

			if w.Code != tt.statusCode {
				t.Errorf("status = %d, want %d", w.Code, tt.statusCode)
			}

			var result map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatalf("failed to parse response: %v", err)
			}

			if result["type"] != "error" {
				t.Errorf("type = %v, want 'error'", result["type"])
			}

			errObj, ok := result["error"].(map[string]any)
			if !ok {
				t.Fatal("expected 'error' key in response")
			}

			if errObj["type"] != tt.wantType {
				t.Errorf("error type = %v, want %v", errObj["type"], tt.wantType)
			}
		})
	}
}

func TestMapErrorType(t *testing.T) {
	tests := []struct {
		code     ErrorCode
		wantType string
	}{
		{ErrInvalidAPIKey, "authentication_error"},
		{ErrModelNotFound, "not_found_error"},
		{ErrInvalidRequest, "invalid_request_error"},
		{ErrUpstreamError, "api_error"},
		{ErrUpstreamTimeout, "api_error"},
		{ErrInternal, "api_error"},
	}

	for _, tt := range tests {
		t.Run(string(tt.code), func(t *testing.T) {
			got := mapErrorType(tt.code)
			if got != tt.wantType {
				t.Errorf("mapErrorType(%q) = %q, want %q", tt.code, got, tt.wantType)
			}
		})
	}
}

func TestGatewayError(t *testing.T) {
	err := New(ErrModelNotFound, "model xyz not found", http.StatusNotFound)

	if err.Code != ErrModelNotFound {
		t.Errorf("code = %v, want %v", err.Code, ErrModelNotFound)
	}
	if err.Message != "model xyz not found" {
		t.Errorf("message = %q, want %q", err.Message, "model xyz not found")
	}
	if err.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want %d", err.StatusCode, http.StatusNotFound)
	}
	if err.Error() != "model xyz not found" {
		t.Errorf("Error() = %q, want %q", err.Error(), "model xyz not found")
	}
}
