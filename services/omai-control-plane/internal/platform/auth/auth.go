package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/omai/backend/internal/domain"
)

type contextKey struct{}

type PermissionResolver interface {
	Permissions(string) ([]string, bool)
}

type Authenticator struct {
	devToken     string
	serviceToken string
	jwtSecret    []byte
	issuer       string
	audience     string
	permissions  PermissionResolver
}

const maxCredentialBytes = 16 << 10

func New(devToken, serviceToken, jwtSecret, issuer, audience string, permissions PermissionResolver) *Authenticator {
	return &Authenticator{
		devToken:     devToken,
		serviceToken: serviceToken,
		jwtSecret:    []byte(jwtSecret),
		issuer:       issuer,
		audience:     audience,
		permissions:  permissions,
	}
}

func PrincipalFromContext(ctx context.Context) (domain.Principal, bool) {
	principal, ok := ctx.Value(contextKey{}).(domain.Principal)
	return principal, ok
}

func ContextWithPrincipal(ctx context.Context, principal domain.Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, principal)
}

func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodOptions || publicProcedure(request.URL.Path) {
			next.ServeHTTP(response, request)
			return
		}
		principal, err := a.authenticate(request)
		if err != nil {
			writeError(response, http.StatusUnauthorized, "unauthenticated")
			return
		}
		if permissions, registered := a.permissions.Permissions(request.URL.Path); protectedRPC(request.URL.Path) {
			if !registered {
				writeError(response, http.StatusForbidden, "procedure is not registered")
				return
			}
			for _, permission := range permissions {
				if !principal.Allows(permission) {
					writeError(response, http.StatusForbidden, "permission denied")
					return
				}
			}
		}
		ctx := context.WithValue(request.Context(), contextKey{}, principal)
		next.ServeHTTP(response, request.WithContext(ctx))
	})
}

func protectedRPC(path string) bool {
	return strings.HasPrefix(path, "/uab.v1.") || strings.HasPrefix(path, "/omai.platform.v1.")
}

func (a *Authenticator) authenticate(request *http.Request) (domain.Principal, error) {
	authorization := request.Header.Get("Authorization")
	if len(authorization) > maxCredentialBytes {
		return domain.Principal{}, errors.New("bearer token is too large")
	}
	token := bearerToken(authorization)
	if token == "" {
		return domain.Principal{}, errors.New("bearer token is required")
	}
	if secureEqual(token, a.serviceToken) {
		tenantID, actorID, err := identityHeaders(request, "system", "service")
		if err != nil {
			return domain.Principal{}, err
		}
		return domain.Principal{
			TenantID:    tenantID,
			ActorID:     actorID,
			Permissions: []string{"*"},
			Service:     true,
		}, nil
	}
	if secureEqual(token, a.devToken) {
		tenantID, actorID, err := identityHeaders(request, "development", "developer")
		if err != nil {
			return domain.Principal{}, err
		}
		permissions := splitPermissions(request.Header.Values("X-OMAI-Permissions"))
		if len(permissions) == 0 {
			permissions = []string{"*"}
		}
		return domain.Principal{
			TenantID:    tenantID,
			ActorID:     actorID,
			Permissions: permissions,
		}, nil
	}
	if len(a.jwtSecret) == 0 {
		return domain.Principal{}, errors.New("invalid token")
	}
	return a.verifyJWT(token, time.Now().UTC())
}

type claims struct {
	Issuer      string          `json:"iss"`
	Audience    json.RawMessage `json:"aud"`
	Subject     string          `json:"sub"`
	TenantID    string          `json:"tenant_id"`
	ActorID     string          `json:"actor_id"`
	Permissions []string        `json:"permissions"`
	ExpiresAt   json.Number     `json:"exp"`
	NotBefore   json.Number     `json:"nbf"`
}

func (a *Authenticator) verifyJWT(token string, now time.Time) (domain.Principal, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return domain.Principal{}, errors.New("malformed JWT")
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return domain.Principal{}, errors.New("malformed JWT header")
	}
	var metadata struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}
	if err := json.Unmarshal(header, &metadata); err != nil || metadata.Algorithm != "HS256" || metadata.Type != "" && !strings.EqualFold(metadata.Type, "JWT") {
		return domain.Principal{}, errors.New("unsupported JWT algorithm")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return domain.Principal{}, errors.New("malformed JWT signature")
	}
	mac := hmac.New(sha256.New, a.jwtSecret)
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return domain.Principal{}, errors.New("invalid JWT signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return domain.Principal{}, errors.New("malformed JWT payload")
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.UseNumber()
	var value claims
	if err := decoder.Decode(&value); err != nil {
		return domain.Principal{}, errors.New("invalid JWT claims")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return domain.Principal{}, errors.New("invalid JWT claims")
	}
	if value.Issuer != a.issuer || !audienceContains(value.Audience, a.audience) {
		return domain.Principal{}, errors.New("JWT issuer or audience mismatch")
	}
	expiresAt, err := value.ExpiresAt.Int64()
	if err != nil || now.Unix() >= expiresAt {
		return domain.Principal{}, errors.New("JWT expired")
	}
	if value.NotBefore != "" {
		notBefore, err := value.NotBefore.Int64()
		if err != nil || now.Add(30*time.Second).Unix() < notBefore {
			return domain.Principal{}, errors.New("JWT is not active")
		}
	}
	actorID := value.ActorID
	if actorID == "" {
		actorID = value.Subject
	}
	if !safeClaim(value.TenantID) || !safeClaim(actorID) || len(value.Permissions) > 256 || !safePermissions(value.Permissions) {
		return domain.Principal{}, errors.New("required identity claims are invalid")
	}
	return domain.Principal{
		TenantID:    value.TenantID,
		ActorID:     actorID,
		Permissions: append([]string(nil), value.Permissions...),
	}, nil
}

func audienceContains(raw json.RawMessage, expected string) bool {
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single == expected
	}
	var multiple []string
	if json.Unmarshal(raw, &multiple) != nil {
		return false
	}
	for _, candidate := range multiple {
		if candidate == expected {
			return true
		}
	}
	return false
}

func bearerToken(header string) string {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}

func secureEqual(left, right string) bool {
	if left == "" || right == "" || len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func identityHeaders(request *http.Request, tenantFallback, actorFallback string) (string, string, error) {
	tenantID, err := identityHeader(request, "X-OMAI-Tenant-ID", tenantFallback)
	if err != nil {
		return "", "", err
	}
	actorID, err := identityHeader(request, "X-OMAI-Actor-ID", actorFallback)
	if err != nil {
		return "", "", err
	}
	return tenantID, actorID, nil
}

func identityHeader(request *http.Request, name, fallback string) (string, error) {
	raw := request.Header.Get(name)
	if raw == "" {
		return fallback, nil
	}
	if strings.TrimSpace(raw) != raw || !safeClaim(raw) {
		return "", fmt.Errorf("invalid %s", name)
	}
	return raw, nil
}

func safeClaim(value string) bool {
	if value == "" || len(value) > 256 || strings.TrimSpace(value) != value {
		return false
	}
	return !strings.ContainsAny(value, "\r\n\x00")
}

func splitPermissions(values []string) []string {
	var result []string
	for _, value := range values {
		for _, permission := range strings.Split(value, ",") {
			permission = strings.TrimSpace(permission)
			if permission != "" && len(permission) <= 128 {
				result = append(result, permission)
				if len(result) == 256 {
					return result
				}
			}
		}
	}
	return result
}

func safePermissions(permissions []string) bool {
	for _, permission := range permissions {
		if permission == "" || len(permission) > 128 || strings.TrimSpace(permission) != permission || strings.ContainsAny(permission, "\r\n\x00") {
			return false
		}
	}
	return true
}

func publicProcedure(path string) bool {
	switch path {
	case "/livez", "/readyz", "/healthz", "/metrics",
		"/uab.v1.ControlPlaneService/Health",
		"/grpc.health.v1.Health/Check", "/grpc.health.v1.Health/Watch":
		return true
	default:
		return false
	}
}

func writeError(response http.ResponseWriter, status int, message string) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_, _ = fmt.Fprintf(response, "{\"error\":%q}\n", message)
}
