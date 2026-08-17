package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBearerMiddlewareFailsClosed(t *testing.T) {
	token := "executor-token-0123456789-abcdef"
	handler := bearerMiddleware(token, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}))
	for name, credential := range map[string]string{
		"missing":   "",
		"wrong":     "Bearer executor-token-0123456789-abcdeg",
		"oversized": strings.Repeat("x", maxAuthorizationBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/rpc", nil)
			request.Header.Set("Authorization", credential)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") == "" {
				t.Fatalf("status=%d challenge=%q", response.Code, response.Header().Get("WWW-Authenticate"))
			}
		})
	}
	request := httptest.NewRequest(http.MethodPost, "/rpc", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("valid token status=%d", response.Code)
	}
}
