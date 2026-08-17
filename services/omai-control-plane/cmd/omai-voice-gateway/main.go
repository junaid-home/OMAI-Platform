package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/omai/backend/internal/platform/telemetry"
	"github.com/omai/backend/internal/voice/control"
	"github.com/omai/backend/internal/voice/protocol"
	"github.com/omai/backend/internal/voice/provider"
	"github.com/omai/backend/internal/voice/provider/gemini"
	"github.com/omai/backend/internal/voice/session"
)

type config struct {
	Address, MetricsAddress, ControlURL, ServiceToken, GeminiEndpoint, GeminiAPIKey, GeminiModel, SystemPrompt string
	Origins                                                                                                    map[string]struct{}
	OriginPatterns                                                                                             []string
	Idle, Heartbeat, ToolTimeout                                                                               time.Duration
}

func main() {
	if err := run(); err != nil {
		slog.Error("OMAI voice gateway stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	cfg, err := load()
	if err != nil {
		return err
	}
	root, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	controlClient := control.New(cfg.ControlURL, cfg.ServiceToken)
	factory := &gemini.Factory{Endpoint: cfg.GeminiEndpoint, APIKey: cfg.GeminiAPIKey, Model: cfg.GeminiModel, SystemPrompt: cfg.SystemPrompt}
	metrics := telemetry.New()
	mux := http.NewServeMux()
	mux.HandleFunc("/livez", func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/readyz", func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/omai/voice/ws", func(response http.ResponseWriter, request *http.Request) {
		handleVoice(root, cfg, controlClient, factory, metrics, response, request)
	})
	server := &http.Server{Addr: cfg.Address, Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 16 << 10}
	metricsMux := http.NewServeMux()
	metricsMux.HandleFunc("/metrics", metrics.Handler)
	metricsServer := &http.Server{Addr: cfg.MetricsAddress, Handler: metricsMux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
	errCh := make(chan error, 2)
	go func() { errCh <- server.ListenAndServe() }()
	go func() { errCh <- metricsServer.ListenAndServe() }()
	select {
	case <-root.Done():
		ctx, done := context.WithTimeout(context.Background(), 15*time.Second)
		defer done()
		return errors.Join(server.Shutdown(ctx), metricsServer.Shutdown(ctx))
	case err := <-errCh:
		shutdown, done := context.WithTimeout(context.Background(), 15*time.Second)
		defer done()
		_ = server.Shutdown(shutdown)
		_ = metricsServer.Shutdown(shutdown)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
func handleVoice(root context.Context, cfg config, controlClient *control.Client, factory provider.Factory, metrics *telemetry.Metrics, response http.ResponseWriter, request *http.Request) {
	if _, ok := cfg.Origins[request.Header.Get("Origin")]; !ok {
		http.Error(response, "forbidden origin", http.StatusForbidden)
		return
	}
	ticket := strings.TrimSpace(request.URL.Query().Get("ticket"))
	if ticket == "" {
		http.Error(response, "ticket required", http.StatusUnauthorized)
		return
	}
	sessionID, err := identifier("vcs_")
	if err != nil {
		http.Error(response, "unavailable", http.StatusServiceUnavailable)
		return
	}
	admissionCtx, cancel := context.WithTimeout(root, 5*time.Second)
	redeemed, err := controlClient.Redeem(admissionCtx, ticket, sessionID)
	cancel()
	if err != nil {
		http.Error(response, "invalid or exhausted voice ticket", http.StatusUnauthorized)
		return
	}
	lease := redeemed.GetLeaseToken()
	defer controlClient.Release(context.Background(), lease)
	toolsCtx, toolsCancel := context.WithTimeout(root, 5*time.Second)
	tools, etag, err := controlClient.Tools(toolsCtx, lease)
	toolsCancel()
	if err != nil {
		http.Error(response, "voice tool catalog unavailable", http.StatusServiceUnavailable)
		return
	}
	connection, err := websocket.Accept(response, request, &websocket.AcceptOptions{OriginPatterns: cfg.OriginPatterns, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	connection.SetReadLimit(128 << 10)
	providerCtx, providerCancel := context.WithCancel(root)
	defer providerCancel()
	providerSession, err := factory.Connect(providerCtx, tools, redeemed.GetClaims().GetVoice())
	if err != nil {
		metrics.VoiceErrors.Add(1)
		data, _ := json.Marshal(protocol.ServerMessage{Type: "error", Message: "voice provider unavailable"})
		_ = connection.Write(providerCtx, websocket.MessageText, data)
		_ = connection.Close(websocket.StatusInternalError, "provider unavailable")
		return
	}
	metrics.VoiceStarted.Add(1)
	metrics.VoiceActive.Add(1)
	defer metrics.VoiceActive.Add(-1)
	ready, _ := json.Marshal(protocol.ServerMessage{Type: "ready", SessionID: sessionID, Provider: providerSession.Name(), Model: providerSession.Model(), RegistryETag: etag, InputSampleRate: providerSession.InputSampleRate(), OutputSampleRate: providerSession.OutputSampleRate()})
	if err := connection.Write(providerCtx, websocket.MessageText, ready); err != nil {
		_ = providerSession.Close()
		return
	}
	voiceSession := session.New(sessionID, lease, connection, providerSession, controlClient, cfg.Idle, cfg.Heartbeat, cfg.ToolTimeout)
	if err := voiceSession.Run(providerCtx); err != nil && !strings.Contains(strings.ToLower(err.Error()), "closed") {
		metrics.VoiceErrors.Add(1)
		slog.Warn("voice session ended", "session_id", sessionID, "error", err)
	}
}
func load() (config, error) {
	result := config{Address: env("OMAI_VOICE_ADDR", "127.0.0.1:8791"), MetricsAddress: env("OMAI_VOICE_METRICS_ADDR", "127.0.0.1:9092"), ControlURL: env("OMAI_CONTROL_URL", "http://127.0.0.1:8787"), ServiceToken: strings.TrimSpace(os.Getenv("OMAI_SERVICE_TOKEN")), GeminiEndpoint: env("OMAI_GEMINI_LIVE_ENDPOINT", "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"), GeminiAPIKey: strings.TrimSpace(os.Getenv("GOOGLE_API_KEY")), GeminiModel: env("OMAI_VOICE_MODEL", "gemini-3.1-flash-live-preview"), SystemPrompt: env("OMAI_VOICE_SYSTEM_PROMPT", "You are OMAI Voice. Use only the provided tools for actions. Never claim success until the tool result confirms it. Ask for confirmation when required. Keep spoken responses precise."), Idle: duration("OMAI_VOICE_IDLE_TIMEOUT", 10*time.Minute), Heartbeat: duration("OMAI_VOICE_HEARTBEAT", 15*time.Second), ToolTimeout: duration("OMAI_VOICE_TOOL_TIMEOUT", 65*time.Second)}
	origins, originPatterns, err := parseOrigins(os.Getenv("OMAI_ALLOWED_ORIGINS"))
	if err != nil {
		return result, err
	}
	result.Origins = origins
	result.OriginPatterns = originPatterns
	if len(result.ServiceToken) < 32 {
		return result, errors.New("OMAI_SERVICE_TOKEN must contain at least 32 characters")
	}
	if len(result.GeminiAPIKey) < 20 {
		return result, errors.New("GOOGLE_API_KEY is required")
	}
	if len(result.Origins) == 0 {
		return result, errors.New("OMAI_ALLOWED_ORIGINS must be explicit")
	}
	if result.Heartbeat <= 0 || result.Idle <= result.Heartbeat {
		return result, errors.New("voice timeout configuration is invalid")
	}
	if _, _, err := net.SplitHostPort(result.Address); err != nil {
		return result, errors.New("OMAI_VOICE_ADDR must be a valid host:port address")
	}
	if _, _, err := net.SplitHostPort(result.MetricsAddress); err != nil {
		return result, errors.New("OMAI_VOICE_METRICS_ADDR must be a valid host:port address")
	}
	if result.Address == result.MetricsAddress {
		return result, errors.New("voice and metrics listeners must use different addresses")
	}
	return result, nil
}
func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
func duration(name string, fallback time.Duration) time.Duration {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil {
			return parsed
		}
	}
	return fallback
}
func identifier(prefix string) (string, error) {
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}
func parseOrigins(raw string) (map[string]struct{}, []string, error) {
	origins := make(map[string]struct{})
	patterns := make([]string, 0)
	seenPatterns := make(map[string]struct{})
	for _, item := range strings.Split(raw, ",") {
		value := strings.TrimSpace(item)
		if value == "" {
			continue
		}
		parsed, err := url.Parse(value)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, nil, fmt.Errorf("OMAI_ALLOWED_ORIGINS contains invalid origin %q", value)
		}
		normalized := parsed.Scheme + "://" + parsed.Host
		origins[normalized] = struct{}{}
		if _, exists := seenPatterns[parsed.Host]; !exists {
			patterns = append(patterns, parsed.Host)
			seenPatterns[parsed.Host] = struct{}{}
		}
	}
	return origins, patterns, nil
}
