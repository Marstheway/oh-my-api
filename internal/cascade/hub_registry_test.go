package cascade

import (
	"testing"
)

func TestHubRegistry_SetGet(t *testing.T) {
	reg := NewHubRegistry()
	if got := reg.Get(); got != nil {
		t.Fatalf("Get() = %v, want nil", got)
	}

	h1 := NewHub(HubConfig{ProviderName: "corp-dev", Token: "t1"})
	reg.Set(h1)
	if got := reg.Get(); got != h1 {
		t.Fatalf("Get() = %p, want %p", got, h1)
	}

	h2 := NewHub(HubConfig{ProviderName: "corp-dev", Token: "t2"})
	reg.Set(h2)
	if got := reg.Get(); got != h2 {
		t.Fatalf("Get() after swap = %p, want %p", got, h2)
	}
	if got := reg.Get(); got == h1 {
		t.Fatal("registry still returns old hub after Set")
	}
}
