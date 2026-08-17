package preview

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"

	"github.com/omai/backend/internal/domain"
)

const GatewayPrefix = "/__omai/preview/"

type PublisherConfig struct {
	PublicBaseURL     string
	PublicURLTemplate string
}

type publishedRoute struct {
	token      string
	publicHost string
	proxy      *httputil.ReverseProxy
}

// Publisher owns unguessable browser routes and proxies HTTP streaming and
// WebSocket upgrades to a private dev-server address. Runtime URLs never cross
// the public RPC boundary.
type Publisher struct {
	mu          sync.RWMutex
	base        *url.URL
	template    string
	hostPrefix  string
	hostSuffix  string
	allowedHost string
	byOwner     map[string]*publishedRoute
	byToken     map[string]*publishedRoute
	byHost      map[string]*publishedRoute
}

func NewPublisher(config PublisherConfig) (*Publisher, error) {
	base, err := parsePublicBase(config.PublicBaseURL)
	if err != nil {
		return nil, err
	}
	template := strings.TrimSpace(config.PublicURLTemplate)
	hostPrefix, hostSuffix, allowedHost := "", "", ""
	if template != "" {
		if strings.Count(template, "{id}") != 1 {
			return nil, fmt.Errorf("%w: preview URL template requires exactly one {id}", domain.ErrInvalid)
		}
		const marker = "omai-preview-token-marker"
		probe, parseErr := url.Parse(strings.Replace(template, "{id}", marker, 1))
		if parseErr != nil || probe.Hostname() == "" || probe.User != nil || probe.RawQuery != "" || probe.Fragment != "" || (probe.Path != "" && probe.Path != "/") || (probe.Scheme != "http" && probe.Scheme != "https") {
			return nil, fmt.Errorf("%w: invalid preview URL template", domain.ErrInvalid)
		}
		var found bool
		hostPrefix, hostSuffix, found = strings.Cut(strings.ToLower(probe.Host), marker)
		_, hostnameSuffix, hostnameFound := strings.Cut(strings.ToLower(probe.Hostname()), marker)
		if !found || !hostnameFound || !strings.HasPrefix(hostnameSuffix, ".") {
			return nil, fmt.Errorf("%w: preview template token must be in the host", domain.ErrInvalid)
		}
		allowedHost = hostnameSuffix
	}
	return &Publisher{
		base: base, template: template, hostPrefix: hostPrefix, hostSuffix: hostSuffix, allowedHost: allowedHost,
		byOwner: make(map[string]*publishedRoute), byToken: make(map[string]*publishedRoute), byHost: make(map[string]*publishedRoute),
	}, nil
}

// AdditionalAllowedHost is the operator-owned wildcard suffix accepted by
// Vite/Astro. It never returns a universal host wildcard.
func (p *Publisher) AdditionalAllowedHost() string { return p.allowedHost }

func (p *Publisher) Publish(_ context.Context, owner, runtimeURL string) (string, error) {
	if strings.TrimSpace(owner) == "" {
		return "", fmt.Errorf("%w: preview route owner required", domain.ErrInvalid)
	}
	target, err := url.Parse(strings.TrimSpace(runtimeURL))
	if err != nil || target.Hostname() == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" || (target.Path != "" && target.Path != "/") || (target.Scheme != "http" && target.Scheme != "https") {
		return "", fmt.Errorf("%w: invalid preview runtime URL", domain.ErrInvalid)
	}
	token, err := routeToken()
	if err != nil {
		return "", err
	}
	publicURL := strings.TrimRight(p.base.String(), "/") + GatewayPrefix + token + "/"
	publicHost := ""
	if p.template != "" {
		publicURL = strings.Replace(p.template, "{id}", token, 1)
		parsed, parseErr := url.Parse(publicURL)
		if parseErr != nil {
			return "", parseErr
		}
		publicHost = strings.ToLower(parsed.Host)
	}
	proxy := &httputil.ReverseProxy{}
	proxy.Rewrite = func(request *httputil.ProxyRequest) {
		browserHost := request.In.Host
		browserScheme := "http"
		if request.In.TLS != nil {
			browserScheme = "https"
		}
		request.SetURL(target)
		request.Out.Host = browserHost
		request.Out.Header.Del("Authorization")
		request.Out.Header.Del("Cookie")
		request.Out.Header.Del("Proxy-Authorization")
		request.Out.Header.Set("X-Forwarded-Host", browserHost)
		request.Out.Header.Set("X-Forwarded-Proto", browserScheme)
		request.Out.Header.Set("X-OMAI-Preview-Route", token)
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		response.Header.Del("Set-Cookie")
		response.Header.Del("Proxy-Authenticate")
		response.Header.Del("Www-Authenticate")
		return nil
	}
	proxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(writer, "preview runtime unavailable", http.StatusBadGateway)
	}
	route := &publishedRoute{token: token, publicHost: publicHost, proxy: proxy}
	p.mu.Lock()
	if previous := p.byOwner[owner]; previous != nil {
		delete(p.byToken, previous.token)
		delete(p.byHost, previous.publicHost)
	}
	p.byOwner[owner] = route
	p.byToken[token] = route
	if publicHost != "" {
		p.byHost[publicHost] = route
	}
	p.mu.Unlock()
	return publicURL, nil
}

func (p *Publisher) Unpublish(_ context.Context, owner string) error {
	p.mu.Lock()
	if route := p.byOwner[owner]; route != nil {
		delete(p.byOwner, owner)
		delete(p.byToken, route.token)
		delete(p.byHost, route.publicHost)
	}
	p.mu.Unlock()
	return nil
}

// Wrap leaves normal RPC paths untouched and treats a known 192-bit route
// token as the preview authorization capability. Unknown preview routes return
// 404 and never fall through into the control-plane mux.
func (p *Publisher) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		route, prefix, previewRequest := p.route(request)
		if !previewRequest {
			next.ServeHTTP(writer, request)
			return
		}
		if route == nil {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		if prefix != "" {
			request = request.Clone(request.Context())
			request.URL.Path = strings.TrimPrefix(request.URL.Path, prefix)
			if request.URL.Path == "" {
				request.URL.Path = "/"
			}
			request.URL.RawPath = ""
			request.Header.Set("X-Forwarded-Prefix", strings.TrimSuffix(prefix, "/"))
		}
		route.proxy.ServeHTTP(writer, request)
	})
}

func (p *Publisher) route(request *http.Request) (*publishedRoute, string, bool) {
	host := strings.ToLower(request.Host)
	p.mu.RLock()
	if route := p.byHost[host]; route != nil {
		p.mu.RUnlock()
		return route, "", true
	}
	p.mu.RUnlock()
	if p.template != "" && host != strings.ToLower(p.base.Host) && strings.HasPrefix(host, p.hostPrefix) && strings.HasSuffix(host, p.hostSuffix) && len(host) > len(p.hostPrefix)+len(p.hostSuffix) {
		return nil, "", true
	}
	if !strings.HasPrefix(request.URL.Path, GatewayPrefix) {
		return nil, "", false
	}
	rest := strings.TrimPrefix(request.URL.Path, GatewayPrefix)
	token, _, _ := strings.Cut(rest, "/")
	if token == "" {
		return nil, "", true
	}
	p.mu.RLock()
	route := p.byToken[token]
	p.mu.RUnlock()
	return route, GatewayPrefix + token, true
}

func parsePublicBase(value string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(value), "/"))
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("%w: invalid preview public base URL", domain.ErrInvalid)
	}
	return parsed, nil
}

func routeToken() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("mint preview route token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
