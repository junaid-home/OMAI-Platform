package preview

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/omai/backend/internal/domain"
	"github.com/omai/backend/internal/port"
)

const maxPreviewResponseBytes = 64 << 20

var forbiddenRequestHeaders = map[string]struct{}{
	"Authorization":       {},
	"Connection":          {},
	"Content-Length":      {},
	"Cookie":              {},
	"Forwarded":           {},
	"Host":                {},
	"Keep-Alive":          {},
	"Proxy-Authorization": {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
}

var forbiddenResponseHeaders = map[string]struct{}{
	"Authentication-Info": {},
	"Connection":          {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Set-Cookie":          {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
	"Www-Authenticate":    {},
}

type Gateway struct {
	base   *url.URL
	client *http.Client
}

func New(rawURL string) (*Gateway, error) {
	if rawURL == "" {
		return &Gateway{}, nil
	}
	base, err := url.Parse(rawURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("invalid preview base URL")
	}
	return &Gateway{base: base, client: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (g *Gateway) Fetch(ctx context.Context, method, path string, headers map[string]string, body io.Reader) (*port.PreviewResponse, error) {
	if g.base == nil {
		return nil, domain.ErrUnavailable
	}
	method = strings.ToUpper(method)
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodPost {
		return nil, fmt.Errorf("%w: preview method", domain.ErrInvalid)
	}
	relative, err := url.Parse(path)
	if err != nil || relative.IsAbs() || relative.Host != "" || !strings.HasPrefix(relative.Path, "/") {
		return nil, fmt.Errorf("%w: preview path", domain.ErrInvalid)
	}
	target := g.base.ResolveReference(relative)
	if target.Host != g.base.Host || target.Scheme != g.base.Scheme {
		return nil, domain.ErrForbidden
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, err
	}
	for name, value := range headers {
		canonical := http.CanonicalHeaderKey(name)
		if _, forbidden := forbiddenRequestHeaders[canonical]; forbidden || strings.HasPrefix(canonical, "X-Forwarded-") || strings.HasPrefix(canonical, "Proxy-") {
			continue
		}
		request.Header.Set(canonical, value)
	}
	response, err := g.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("preview upstream: %w", err)
	}
	responseHeaders := response.Header.Clone()
	for name := range responseHeaders {
		canonical := http.CanonicalHeaderKey(name)
		if _, forbidden := forbiddenResponseHeaders[canonical]; forbidden || strings.HasPrefix(canonical, "Proxy-") {
			responseHeaders.Del(name)
		}
	}
	return &port.PreviewResponse{
		Status: response.StatusCode,
		Header: responseHeaders,
		Body:   &limitedResponseBody{source: response.Body, remaining: maxPreviewResponseBytes},
	}, nil
}

type limitedResponseBody struct {
	source    io.ReadCloser
	remaining int64
	exhausted bool
}

func (body *limitedResponseBody) Read(destination []byte) (int, error) {
	if body.exhausted {
		return 0, fmt.Errorf("preview response exceeds %d bytes", maxPreviewResponseBytes)
	}
	if int64(len(destination)) > body.remaining+1 {
		destination = destination[:body.remaining+1]
	}
	read, err := body.source.Read(destination)
	if int64(read) > body.remaining {
		read = int(body.remaining)
		body.remaining = 0
		body.exhausted = true
		return read, fmt.Errorf("preview response exceeds %d bytes", maxPreviewResponseBytes)
	}
	body.remaining -= int64(read)
	return read, err
}

func (body *limitedResponseBody) Close() error {
	return body.source.Close()
}
