package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestAuthenticateRejectsWrongAndOversizedCredentials(t *testing.T) {
	t.Parallel()

	const token = "01234567890123456789012345678901"
	handler := authenticate(token, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}))
	for name, credential := range map[string]string{
		"missing":   "",
		"wrong":     "Bearer 01234567890123456789012345678902",
		"oversized": strings.Repeat("x", maxBearerBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/", nil)
			request.Header.Set("Authorization", credential)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
			}
			if response.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("missing WWW-Authenticate challenge")
			}
		})
	}

	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("valid credential status = %d, want %d", response.Code, http.StatusNoContent)
	}
}

func TestTextStreamStateSuppressesFinalReplay(t *testing.T) {
	t.Parallel()

	state := &textStreamState{}
	if !state.shouldEmit(true, &genai.Part{Text: "first delta"}) {
		t.Fatal("partial answer was suppressed")
	}
	if state.shouldEmit(false, &genai.Part{Text: "first delta and final"}) {
		t.Fatal("final cumulative answer replay was emitted")
	}
	if !state.shouldEmit(false, &genai.Part{Text: "thought", Thought: true}) {
		t.Fatal("independent final thought was suppressed")
	}

	nonStreaming := &textStreamState{}
	if !nonStreaming.shouldEmit(false, &genai.Part{Text: "only final"}) {
		t.Fatal("non-streaming final answer was suppressed")
	}
}

func TestRuntimeMessageID(t *testing.T) {
	t.Parallel()

	first, err := runtimeMessageID()
	if err != nil {
		t.Fatalf("runtimeMessageID() error = %v", err)
	}
	second, err := runtimeMessageID()
	if err != nil {
		t.Fatalf("runtimeMessageID() second error = %v", err)
	}
	if len(first) != 36 || first[:4] != "msg_" || first == second {
		t.Fatalf("runtime message identifiers are invalid: %q %q", first, second)
	}
}

func TestValidateIdentity(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", " tenant", "tenant\x00", strings.Repeat("x", maxIdentityBytes+1)} {
		if err := validateIdentity("tenant_id", value); err == nil {
			t.Fatalf("validateIdentity(%q) accepted an unsafe value", value)
		}
	}
	if err := validateIdentity("tenant_id", "tenant-a"); err != nil {
		t.Fatalf("validateIdentity rejected a safe value: %v", err)
	}
}

func TestLoopbackAddress(t *testing.T) {
	t.Parallel()
	if !loopbackAddress("127.0.0.1:8790") || !loopbackAddress("[::1]:8790") {
		t.Fatal("loopback listener was rejected")
	}
	if loopbackAddress("0.0.0.0:8790") || loopbackAddress("10.0.0.1:8790") {
		t.Fatal("non-loopback listener was accepted")
	}
}

func TestValidateTransportPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		environment   string
		address       string
		tlsCert       string
		clientCA      string
		allowInsecure bool
		wantError     bool
	}{
		{name: "development loopback", environment: "development", address: "127.0.0.1:8790"},
		{name: "development private network denied by default", environment: "development", address: "0.0.0.0:8790", wantError: true},
		{name: "development private network explicit opt in", environment: "development", address: "0.0.0.0:8790", allowInsecure: true},
		{name: "production rejects cleartext override", environment: "production", address: "0.0.0.0:8790", allowInsecure: true, wantError: true},
		{name: "production mutual TLS", environment: "production", address: "0.0.0.0:8790", tlsCert: "server.crt", clientCA: "clients.crt"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateTransportPolicy(test.environment, test.address, test.tlsCert, test.clientCA, test.allowInsecure)
			if (err != nil) != test.wantError {
				t.Fatalf("validateTransportPolicy() error = %v, wantError = %v", err, test.wantError)
			}
		})
	}
}
