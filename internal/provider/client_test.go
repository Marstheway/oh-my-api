package provider

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/config"
)

func TestDo_NormalProvider_NoBridgeHeaders(t *testing.T) {
	var capturedReq *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedReq = r
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"openai": {Endpoint: srv.URL, APIKey: "sk-test", Protocols: []string{"openai.chat"}},
	}

	client := NewClient(providers, 5*time.Second, 0, 0)

	req, _ := http.NewRequest("GET", srv.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-test")

	resp, err := client.Do("openai", req)
	if err != nil {
		t.Fatalf("Do error: %v", err)
	}
	resp.Body.Close()

	if capturedReq == nil {
		t.Fatal("no request received")
	}

	if got := capturedReq.Header.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("Authorization header = %q, want %q", got, "Bearer sk-test")
	}
	if got := capturedReq.Header.Get("X-Oh-My-API-Bridge-Provider"); got != "" {
		t.Errorf("X-Oh-My-API-Bridge-Provider = %q, want empty for normal provider", got)
	}
}

func TestDo_RemoteBridgeProvider_InjectsHeaders(t *testing.T) {
	var capturedReq *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedReq = r
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"bridge-xai": {
			Endpoint:  srv.URL,
			APIKey:    "",
			Protocols: []string{"openai.responses"},
			RemoteBridge: &config.RemoteBridgeConfig{
				Enabled:  true,
				Provider: "xai-oauth",
				Token:    "bridge-secret-token",
			},
		},
	}

	client := NewClient(providers, 5*time.Second, 0, 0)

	req, _ := http.NewRequest("POST", srv.URL+"/v1/responses", nil)

	resp, err := client.Do("bridge-xai", req)
	if err != nil {
		t.Fatalf("Do error: %v", err)
	}
	resp.Body.Close()

	if capturedReq == nil {
		t.Fatal("no request received")
	}

	if got := capturedReq.Header.Get("Authorization"); got != "Bearer bridge-secret-token" {
		t.Errorf("Authorization header = %q, want %q", got, "Bearer bridge-secret-token")
	}
	if got := capturedReq.Header.Get("X-Oh-My-API-Bridge-Provider"); got != "xai-oauth" {
		t.Errorf("X-Oh-My-API-Bridge-Provider = %q, want %q", got, "xai-oauth")
	}
}
