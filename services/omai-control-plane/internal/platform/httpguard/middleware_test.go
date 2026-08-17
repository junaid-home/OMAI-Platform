package httpguard

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/omai/backend/internal/platform/auth"
	"github.com/omai/backend/internal/platform/telemetry"
)

type registeredPermissions struct{}

func (registeredPermissions) Permissions(string) ([]string, bool) { return nil, true }

func TestIngressRateLimitCoversInvalidCredentialRotation(t *testing.T) {
	t.Parallel()
	authenticator := auth.New("valid-development-token-0123456789", "", "", "", "", registeredPermissions{})
	handler := New([]string{"https://app.example"}, 1, 1, telemetry.New()).Middleware(authenticator.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))

	first := httptest.NewRequest(http.MethodPost, "/omai.platform.v1.TestService/Call", nil)
	first.RemoteAddr = "192.0.2.10:50000"
	first.Header.Set("Authorization", "Bearer invalid-one")
	firstResponse := httptest.NewRecorder()
	handler.ServeHTTP(firstResponse, first)
	if firstResponse.Code != http.StatusUnauthorized {
		t.Fatalf("first status = %d, want 401", firstResponse.Code)
	}

	second := httptest.NewRequest(http.MethodPost, "/omai.platform.v1.TestService/Call", nil)
	second.RemoteAddr = "192.0.2.10:50001"
	second.Header.Set("Authorization", "Bearer invalid-two")
	secondResponse := httptest.NewRecorder()
	handler.ServeHTTP(secondResponse, second)
	if secondResponse.Code != http.StatusTooManyRequests {
		t.Fatalf("rotated invalid credential bypassed rate limit: status = %d", secondResponse.Code)
	}
}

func TestClientKeyDoesNotTrustAttackerCredential(t *testing.T) {
	t.Parallel()
	first := httptest.NewRequest(http.MethodGet, "/livez", nil)
	first.RemoteAddr = "[2001:db8::1]:1000"
	first.Header.Set("Authorization", "Bearer one")
	second := httptest.NewRequest(http.MethodGet, "/livez", nil)
	second.RemoteAddr = "[2001:db8::1]:2000"
	second.Header.Set("Authorization", "Bearer two")
	if clientKey(first) != clientKey(second) {
		t.Fatalf("credential rotation changed client key: %q != %q", clientKey(first), clientKey(second))
	}
}

func TestBucketCardinalityIsBounded(t *testing.T) {
	t.Parallel()
	guard := New(nil, 1, 1, telemetry.New())
	now := time.Now()
	for index := 0; index < maxClientBuckets; index++ {
		if !guard.allow("client-"+strconv.Itoa(index), now) {
			t.Fatalf("bucket %d was rejected before the cap", index)
		}
	}
	if guard.allow("overflow", now) {
		t.Fatal("new bucket was accepted after the cap")
	}
	if len(guard.buckets) != maxClientBuckets {
		t.Fatalf("bucket count = %d", len(guard.buckets))
	}
}
