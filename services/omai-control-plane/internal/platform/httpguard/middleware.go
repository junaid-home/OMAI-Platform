package httpguard

import (
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"github.com/omai/backend/internal/platform/telemetry"
)

const maxClientBuckets = 10_000

type Guard struct {
	origins map[string]struct{}
	rate    float64
	burst   float64
	metrics *telemetry.Metrics
	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
	seen   time.Time
}

func New(origins []string, rate, burst float64, metrics *telemetry.Metrics) *Guard {
	allowed := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		allowed[origin] = struct{}{}
	}
	return &Guard{origins: allowed, rate: rate, burst: burst, metrics: metrics, buckets: make(map[string]*bucket)}
}

func (g *Guard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		g.metrics.Requests.Add(1)
		defer func() {
			if recovered := recover(); recovered != nil {
				g.metrics.Errors.Add(1)
				slog.Error("panic recovered from HTTP request", "path", request.URL.Path, "panic", recovered, "stack", string(debug.Stack()))
				http.Error(response, "internal server error", http.StatusInternalServerError)
			}
		}()
		origin := request.Header.Get("Origin")
		if origin != "" {
			if _, allowed := g.origins[origin]; !allowed {
				g.metrics.Errors.Add(1)
				http.Error(response, "origin denied", http.StatusForbidden)
				return
			}
			response.Header().Set("Access-Control-Allow-Origin", origin)
			response.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
			response.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Connect-Protocol-Version, Connect-Timeout-Ms, X-OMAI-Actor-ID, X-OMAI-Tenant-ID, X-OMAI-Permissions")
			response.Header().Set("Access-Control-Expose-Headers", "Grpc-Status, Grpc-Message, Connect-Content-Encoding")
			response.Header().Set("Vary", "Origin")
		}
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Frame-Options", "DENY")
		if request.Method == http.MethodOptions {
			response.WriteHeader(http.StatusNoContent)
			return
		}
		if !g.allow(clientKey(request), time.Now()) {
			g.metrics.RateLimited.Add(1)
			response.Header().Set("Retry-After", "1")
			http.Error(response, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(response, request)
	})
}

func (g *Guard) allow(key string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	entry := g.buckets[key]
	if entry == nil {
		if len(g.buckets) >= maxClientBuckets {
			g.prune(now.Add(-10 * time.Minute))
			if len(g.buckets) >= maxClientBuckets {
				return false
			}
		}
		entry = &bucket{tokens: g.burst, last: now}
		g.buckets[key] = entry
	}
	entry.tokens += now.Sub(entry.last).Seconds() * g.rate
	if entry.tokens > g.burst {
		entry.tokens = g.burst
	}
	entry.last = now
	entry.seen = now
	if entry.tokens < 1 {
		return false
	}
	entry.tokens--
	return true
}

func (g *Guard) prune(cutoff time.Time) {
	for candidate, value := range g.buckets {
		if value.seen.Before(cutoff) {
			delete(g.buckets, candidate)
		}
	}
}

func clientKey(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err == nil {
		if ip := net.ParseIP(host); ip != nil {
			return "ip:" + ip.String()
		}
		return "ip:" + host
	}
	return "ip:" + request.RemoteAddr
}
