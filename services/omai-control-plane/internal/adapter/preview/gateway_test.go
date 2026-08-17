package preview

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewRejectsAuthorityAndAmbiguousBaseURL(t *testing.T) {
	t.Parallel()

	for _, rawURL := range []string{
		"file:///tmp/preview",
		"http://user:password@example.test",
		"http://example.test?target=elsewhere",
		"http://example.test#fragment",
	} {
		if _, err := New(rawURL); err == nil {
			t.Fatalf("New(%q) accepted an unsafe preview URL", rawURL)
		}
	}
}

func TestFetchStripsCredentialsAndHopByHopHeaders(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		for _, name := range []string{"Authorization", "Cookie", "Forwarded", "X-Forwarded-For", "Proxy-Authorization", "Upgrade"} {
			if request.Header.Get(name) != "" {
				t.Errorf("upstream received forbidden header %s", name)
			}
		}
		if request.Header.Get("X-OMAI-Test") != "safe" {
			t.Errorf("upstream safe header = %q", request.Header.Get("X-OMAI-Test"))
		}
		response.Header().Set("Set-Cookie", "session=secret")
		response.Header().Set("X-OMAI-Test", "safe")
		_, _ = io.WriteString(response, "preview")
	}))
	t.Cleanup(upstream.Close)

	gateway, err := New(upstream.URL)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := gateway.Fetch(context.Background(), http.MethodGet, "/", map[string]string{
		"Authorization":       "Bearer secret",
		"Cookie":              "session=secret",
		"Forwarded":           "for=attacker",
		"X-Forwarded-For":     "attacker",
		"Proxy-Authorization": "secret",
		"Upgrade":             "websocket",
		"X-OMAI-Test":         "safe",
	}, nil)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	defer result.Body.Close()
	responseHeaders := http.Header(result.Header)
	if responseHeaders.Get("Set-Cookie") != "" {
		t.Fatal("Fetch returned an upstream Set-Cookie header")
	}
	if responseHeaders.Get("X-OMAI-Test") != "safe" {
		t.Fatalf("safe response header = %q", responseHeaders.Get("X-OMAI-Test"))
	}
}

func TestLimitedResponseBodyRejectsExcessData(t *testing.T) {
	t.Parallel()

	body := &limitedResponseBody{source: io.NopCloser(strings.NewReader("12345")), remaining: 4}
	data, err := io.ReadAll(body)
	if err == nil {
		t.Fatal("ReadAll accepted a response beyond its limit")
	}
	if string(data) != "1234" {
		t.Fatalf("body = %q, want %q", data, "1234")
	}
}
