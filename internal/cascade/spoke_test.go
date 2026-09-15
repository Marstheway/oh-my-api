package cascade

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestHubWSURL(t *testing.T) {
	tests := []struct {
		origin string
		want   string
	}{
		{"https://api.example.com", "wss://api.example.com/cascade"},
		{"http://localhost:8080", "ws://localhost:8080/cascade"},
	}

	for _, tt := range tests {
		got, err := HubWSURL(tt.origin)
		if err != nil {
			t.Fatalf("origin %q: %v", tt.origin, err)
		}
		if got != tt.want {
			t.Fatalf("origin %q: got %q, want %q", tt.origin, got, tt.want)
		}
	}
}

func TestSpoke_DialAndRegister(t *testing.T) {
	hub := NewHub(HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	server := startTestHub(t, hub)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/cascade"
	spoke := NewSpoke(SpokeConfig{
		URL:   wsURL,
		Token: "hub-secret",
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := spoke.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer spoke.Close()

	if !spoke.Registered() {
		t.Fatal("expected spoke to be registered")
	}
	if _, ok := hub.Session(); !ok {
		t.Fatal("expected hub session after spoke connect")
	}
}

func TestSpoke_ReconnectWithBackoff(t *testing.T) {
	delays := []time.Duration{}
	spoke := NewSpoke(SpokeConfig{
		URL:   "ws://127.0.0.1:1/cascade",
		Token: "unused",
	}, nil)
	spoke.testBackoff = func(d time.Duration) {
		delays = append(delays, d)
	}
	spoke.testDial = func(context.Context) error {
		return context.DeadlineExceeded
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()

	_ = spoke.Run(ctx)

	if len(delays) < 2 {
		t.Fatalf("expected at least 2 backoff delays, got %v", delays)
	}
	if delays[1] <= delays[0] {
		t.Fatalf("expected increasing backoff, got %v", delays)
	}
}
