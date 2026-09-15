package cascade

import (
	"encoding/json"
	"testing"
)

func TestEncodeDecodeRegisterFrame(t *testing.T) {
	original := Frame{
		Type:  FrameRegister,
		Token: "secret-token",
	}

	data, err := EncodeFrame(original)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	decoded, err := DecodeFrame(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Type != FrameRegister || decoded.Token != "secret-token" {
		t.Fatalf("decoded = %+v", decoded)
	}
}

func TestEncodeDecodeJobFrame(t *testing.T) {
	body := json.RawMessage(`{"model":"gpt-4o","messages":[]}`)
	stream := true
	original := Frame{
		Type:     FrameJob,
		ID:       "job-1",
		Protocol: "openai.chat",
		Model:    "corp-dev/gpt-4o",
		Stream:   &stream,
		Body:     body,
		Hop:      "hub",
	}

	data, err := EncodeFrame(original)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	decoded, err := DecodeFrame(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.ID != "job-1" || decoded.Protocol != "openai.chat" || decoded.Model != "corp-dev/gpt-4o" {
		t.Fatalf("decoded = %+v", decoded)
	}
	if decoded.Stream == nil || !*decoded.Stream {
		t.Fatalf("stream = %v, want true", decoded.Stream)
	}
	if string(decoded.Body) != string(body) {
		t.Fatalf("body = %s, want %s", decoded.Body, body)
	}
}

func TestEncodeDecodeCancelFrame(t *testing.T) {
	original := Frame{
		Type: FrameCancel,
		ID:   "job-42",
	}

	data, err := EncodeFrame(original)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	decoded, err := DecodeFrame(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Type != FrameCancel || decoded.ID != "job-42" {
		t.Fatalf("decoded = %+v", decoded)
	}
}

func TestDecodeFrame_RejectsUnknownTypes(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{name: "connect", payload: `{"type":"connect","url":"http://evil"}`},
		{name: "proxy", payload: `{"type":"proxy","url":"http://evil"}`},
		{name: "tunnel", payload: `{"type":"tunnel","url":"http://evil"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeFrame([]byte(tt.payload))
			if err == nil {
				t.Fatalf("expected %q frame to be rejected", tt.name)
			}
		})
	}
}

func TestTokensEqual_LengthMismatchFails(t *testing.T) {
	if tokensEqual("short", "much-longer-token") {
		t.Fatal("expected length mismatch to fail")
	}
	if !tokensEqual("same-token", "same-token") {
		t.Fatal("expected equal tokens to match")
	}
}
