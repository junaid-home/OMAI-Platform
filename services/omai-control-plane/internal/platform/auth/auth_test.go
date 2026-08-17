package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProtectedRPCNamespaces(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"/uab.v1.ControlPlaneService/Prompt",
		"/omai.platform.v1.SessionService/SubmitText",
	} {
		if !protectedRPC(path) {
			t.Fatalf("expected protected RPC namespace for %q", path)
		}
	}
	for _, path := range []string{"/livez", "/grpc.health.v1.Health/Check", "/uab.v10.Fake/Call"} {
		if protectedRPC(path) {
			t.Fatalf("unexpected protected RPC namespace for %q", path)
		}
	}
}

func TestJWTValidationRejectsTrailingClaimsDocument(t *testing.T) {
	t.Parallel()

	const secret = "jwt-secret-0123456789012345678901"
	authenticator := New("", "", secret, "issuer", "audience", nil)
	validPayload := `{"iss":"issuer","aud":"audience","tenant_id":"tenant","actor_id":"actor","permissions":["project:read"],"exp":` + fmt.Sprint(time.Now().Add(time.Hour).Unix()) + `}`
	validRequest := httptest.NewRequest(http.MethodPost, "/omai.platform.v1.Test/Call", nil)
	validRequest.Header.Set("Authorization", "Bearer "+signedJWT(secret, validPayload))
	if _, err := authenticator.authenticate(validRequest); err != nil {
		t.Fatalf("valid JWT rejected: %v", err)
	}

	trailingRequest := httptest.NewRequest(http.MethodPost, "/omai.platform.v1.Test/Call", nil)
	trailingRequest.Header.Set("Authorization", "Bearer "+signedJWT(secret, validPayload+` {}`))
	if _, err := authenticator.authenticate(trailingRequest); err == nil {
		t.Fatal("JWT with a trailing claims document was accepted")
	}
}

func signedJWT(secret, payload string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	body := base64.RawURLEncoding.EncodeToString([]byte(payload))
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(header + "." + body))
	return header + "." + body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestDevelopmentIdentityHeadersFailClosed(t *testing.T) {
	t.Parallel()
	authenticator := New("development-token-0123456789-abcd", "", "", "", "", nil)
	request := httptest.NewRequest(http.MethodPost, "/omai.platform.v1.Test/Call", nil)
	request.Header.Set("Authorization", "Bearer development-token-0123456789-abcd")
	request.Header.Set("X-OMAI-Tenant-ID", " tenant")
	if _, err := authenticator.authenticate(request); err == nil {
		t.Fatal("invalid tenant header silently fell back to the development tenant")
	}
}

func TestOversizedCredentialIsRejected(t *testing.T) {
	t.Parallel()
	authenticator := New("development-token-0123456789-abcd", "", "", "", "", nil)
	request := httptest.NewRequest(http.MethodPost, "/omai.platform.v1.Test/Call", nil)
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("x", maxCredentialBytes))
	if _, err := authenticator.authenticate(request); err == nil {
		t.Fatal("oversized credential was accepted")
	}
}

func TestDevelopmentPermissionsAreBounded(t *testing.T) {
	t.Parallel()
	values := make([]string, 300)
	for index := range values {
		values[index] = "permission-" + strings.Repeat("x", index%3)
	}
	if got := len(splitPermissions([]string{strings.Join(values, ",")})); got != 256 {
		t.Fatalf("permission count = %d, want 256", got)
	}
}

func FuzzBearerAndJWTParsingNeverPanics(f *testing.F) {
	for _, seed := range []string{"", "Bearer token", "bearer token", "Bearer a.b.c", "Basic secret", strings.Repeat("x", 1024)} {
		f.Add(seed)
	}
	authenticator := New("development-token-0123456789-abcd", "", "jwt-secret-0123456789012345678901", "issuer", "audience", nil)
	f.Fuzz(func(t *testing.T, authorization string) {
		request := httptest.NewRequest(http.MethodPost, "/omai.platform.v1.Test/Call", nil)
		request.Header.Set("Authorization", authorization)
		_, _ = authenticator.authenticate(request)
	})
}
