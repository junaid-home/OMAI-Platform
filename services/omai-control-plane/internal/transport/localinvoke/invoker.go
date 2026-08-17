package localinvoke

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"

	"github.com/omai/backend/internal/domain"
	"github.com/omai/backend/internal/platform/auth"
)

type Invoker struct {
	mu        sync.RWMutex
	handler   http.Handler
	maxResult int64
}

func New(maxResult int64) *Invoker                 { return &Invoker{maxResult: maxResult} }
func (i *Invoker) SetHandler(handler http.Handler) { i.mu.Lock(); i.handler = handler; i.mu.Unlock() }
func (i *Invoker) Invoke(ctx context.Context, principal domain.Principal, procedure string, body []byte) ([]byte, error) {
	i.mu.RLock()
	handler := i.handler
	i.mu.RUnlock()
	if handler == nil {
		return nil, domain.ErrUnavailable
	}
	request := httptest.NewRequest(http.MethodPost, procedure, bytes.NewReader(body)).WithContext(auth.ContextWithPrincipal(ctx, principal))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Connect-Protocol-Version", "1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if int64(response.Body.Len()) > i.maxResult {
		return nil, domain.ErrOutputTruncated
	}
	if response.Code < 200 || response.Code >= 300 {
		return nil, fmt.Errorf("tool procedure %s returned HTTP %d: %s", procedure, response.Code, response.Body.String())
	}
	return append([]byte(nil), response.Body.Bytes()...), nil
}
