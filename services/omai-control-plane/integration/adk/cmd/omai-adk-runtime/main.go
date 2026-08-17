package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpchealth"
	"connectrpc.com/grpcreflect"
	uabv1 "github.com/omai/backend/gen/go/uab/v1"
	"github.com/omai/backend/gen/go/uab/v1/uabv1connect"
	"github.com/omai/backend/integration/adk/internal/modelgateway"
	"github.com/omai/backend/integration/adk/internal/modelrouter"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/genai"
)

const (
	maxIdentityBytes = 256
	maxPromptBytes   = 4 << 20
	maxBearerBytes   = 16 << 10
)

var version = "dev"

type runRegistry struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func newRunRegistry() *runRegistry {
	return &runRegistry{cancels: make(map[string]context.CancelFunc)}
}

func (r *runRegistry) Begin(sessionID string, cancel context.CancelFunc) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.cancels[sessionID]; exists {
		return false
	}
	r.cancels[sessionID] = cancel
	return true
}

func (r *runRegistry) Finish(sessionID string) {
	r.mu.Lock()
	delete(r.cancels, sessionID)
	r.mu.Unlock()
}

func (r *runRegistry) Cancel(sessionID string) bool {
	r.mu.Lock()
	cancel := r.cancels[sessionID]
	r.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

type runtimeService struct {
	runner *runner.Runner
	router *modelrouter.Router
	runs   *runRegistry
}

func main() {
	if err := run(); err != nil {
		slog.Error("ADK runtime stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	token := strings.TrimSpace(os.Getenv("OMAI_RUNTIME_TOKEN"))
	if len(token) < 32 {
		return errors.New("OMAI_RUNTIME_TOKEN must contain at least 32 characters")
	}
	environment := env("OMAI_ADK_ENV", "development")
	address := env("OMAI_ADK_ADDR", "127.0.0.1:8790")
	tlsCert := strings.TrimSpace(os.Getenv("OMAI_ADK_TLS_CERT"))
	tlsKey := strings.TrimSpace(os.Getenv("OMAI_ADK_TLS_KEY"))
	clientCA := strings.TrimSpace(os.Getenv("OMAI_ADK_CLIENT_CA"))
	allowInsecure, err := strconv.ParseBool(env("OMAI_ADK_ALLOW_INSECURE", "false"))
	if err != nil {
		return errors.New("OMAI_ADK_ALLOW_INSECURE must be true or false")
	}
	if _, _, err := net.SplitHostPort(address); err != nil {
		return fmt.Errorf("OMAI_ADK_ADDR: %w", err)
	}
	if (tlsCert == "") != (tlsKey == "") {
		return errors.New("OMAI_ADK_TLS_CERT and OMAI_ADK_TLS_KEY must be set together")
	}
	if clientCA != "" && tlsCert == "" {
		return errors.New("OMAI_ADK_CLIENT_CA requires a TLS certificate and key")
	}
	if err := validateTransportPolicy(environment, address, tlsCert, clientCA, allowInsecure); err != nil {
		return err
	}
	providerConfig, err := modelrouter.Load(env("OMAI_ADK_PROVIDERS_FILE", "./config/providers.example.json"))
	if err != nil {
		return err
	}
	modelRouter := modelrouter.New(providerConfig)
	agentInstance, err := llmagent.New(llmagent.Config{
		Name:        "omai_agent",
		Description: "OMAI Go ADK runtime",
		Instruction: env("OMAI_ADK_INSTRUCTION", "You are OMAI, a precise software engineering assistant."),
		Model:       modelRouter,
	})
	if err != nil {
		return fmt.Errorf("create ADK agent: %w", err)
	}
	runnerInstance, err := runner.NewInMemory("omai", agentInstance)
	if err != nil {
		return fmt.Errorf("create ADK runner: %w", err)
	}
	service := &runtimeService{
		runner: runnerInstance,
		router: modelRouter,
		runs:   newRunRegistry(),
	}
	mux := http.NewServeMux()
	runtimePath, runtimeHandler := uabv1connect.NewAgentRuntimeServiceHandler(service)
	mux.Handle(runtimePath, authenticate(token, runtimeHandler))
	modelPath, modelHandler := uabv1connect.NewModelGatewayServiceHandler(modelgateway.New(modelRouter, version))
	mux.Handle(modelPath, authenticate(token, modelHandler))
	services := []string{uabv1connect.AgentRuntimeServiceName, uabv1connect.ModelGatewayServiceName}
	healthPath, healthHandler := grpchealth.NewHandler(grpchealth.NewStaticChecker(services...))
	mux.Handle(healthPath, authenticate(token, healthHandler))
	reflector := grpcreflect.NewStaticReflector(services...)
	reflectionPath, reflectionHandler := grpcreflect.NewHandlerV1(reflector)
	mux.Handle(reflectionPath, authenticate(token, reflectionHandler))
	reflectionAlphaPath, reflectionAlphaHandler := grpcreflect.NewHandlerV1Alpha(reflector)
	mux.Handle(reflectionAlphaPath, authenticate(token, reflectionAlphaHandler))
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(tlsCert == "")
	server := &http.Server{
		Addr:              address,
		Handler:           http.MaxBytesHandler(mux, 48<<20),
		Protocols:         protocols,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 << 10,
	}

	errCh := make(chan error, 1)
	go func() {
		if tlsCert == "" {
			errCh <- server.ListenAndServe()
			return
		}
		tlsConfig, err := adkTLSConfig(tlsCert, tlsKey, clientCA)
		if err != nil {
			errCh <- err
			return
		}
		server.TLSConfig = tlsConfig
		errCh <- server.ListenAndServeTLS("", "")
	}()
	stop, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	select {
	case <-stop.Done():
		shutdown, done := context.WithTimeout(context.Background(), 15*time.Second)
		defer done()
		return server.Shutdown(shutdown)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func adkTLSConfig(certificatePath, keyPath, clientCAPath string) (*tls.Config, error) {
	certificate, err := tls.LoadX509KeyPair(certificatePath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load ADK TLS certificate: %w", err)
	}
	config := &tls.Config{
		MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate},
		NextProtos: []string{"h2", "http/1.1"},
	}
	if clientCAPath == "" {
		return config, nil
	}
	// #nosec G304,G703 -- client CA path is operator-owned runtime configuration.
	data, err := os.ReadFile(clientCAPath)
	if err != nil {
		return nil, fmt.Errorf("read ADK client CA: %w", err)
	}
	clients := x509.NewCertPool()
	if !clients.AppendCertsFromPEM(data) {
		return nil, errors.New("ADK client CA contains no certificates")
	}
	config.ClientCAs = clients
	config.ClientAuth = tls.RequireAndVerifyClientCert
	return config, nil
}

func loopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validateTransportPolicy(environment, address, tlsCert, clientCA string, allowInsecure bool) error {
	if environment == "production" {
		if tlsCert == "" || clientCA == "" {
			return errors.New("production ADK runtime requires mutual TLS")
		}
		return nil
	}
	if tlsCert == "" && !loopbackAddress(address) && !allowInsecure {
		return errors.New("cleartext ADK listener must use loopback unless OMAI_ADK_ALLOW_INSECURE is explicitly enabled")
	}
	return nil
}

func (s *runtimeService) RuntimeHealth(context.Context, *connect.Request[uabv1.AgentRuntimeHealthRequest]) (*connect.Response[uabv1.AgentRuntimeHealthResponse], error) {
	available, authenticated, reason := s.router.Ready()
	return connect.NewResponse(&uabv1.AgentRuntimeHealthResponse{
		Available:     available,
		Authenticated: authenticated,
		Version:       version,
		Reason:        reason,
	}), nil
}

func (s *runtimeService) Run(ctx context.Context, request *connect.Request[uabv1.RuntimePrompt], stream *connect.ServerStream[uabv1.RuntimeEvent]) error {
	message := request.Msg
	if err := validateIdentity("tenant_id", message.GetTenantId()); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := validateIdentity("actor_id", message.GetActorId()); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := validateIdentity("session_id", message.GetSessionId()); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	if len(message.GetText()) == 0 || len(message.GetText()) > maxPromptBytes {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("text must contain between 1 and %d bytes", maxPromptBytes))
	}
	route, err := s.router.Resolve(message.GetProviderId(), message.GetModelId())
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	messageID, err := runtimeMessageID()
	if err != nil {
		return connect.NewError(connect.CodeInternal, errors.New("create runtime message identifier"))
	}
	runCtx, cancel := context.WithCancel(ctx)
	runCtx = modelrouter.WithRoute(runCtx, route.ProviderID, route.ModelID)
	if !s.runs.Begin(message.GetSessionId(), cancel) {
		cancel()
		return connect.NewError(connect.CodeAlreadyExists, errors.New("session already has an active turn"))
	}
	defer func() {
		s.runs.Finish(message.GetSessionId())
		cancel()
	}()

	events := s.runner.Run(
		runCtx,
		message.GetTenantId(),
		message.GetSessionId(),
		genai.NewContentFromText(message.GetText(), genai.RoleUser),
		agent.RunConfig{StreamingMode: agent.StreamingModeSSE},
	)
	streamedText := textStreamState{}
	for event, eventErr := range events {
		if eventErr != nil {
			if errors.Is(runCtx.Err(), context.Canceled) {
				return connect.NewError(connect.CodeCanceled, context.Canceled)
			}
			return connect.NewError(connect.CodeInternal, eventErr)
		}
		if event == nil || event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			emitText := streamedText.shouldEmit(event.Partial, part)
			if err := sendPart(stream, messageID, part, emitText); err != nil {
				return err
			}
		}
	}
	if errors.Is(runCtx.Err(), context.Canceled) {
		return connect.NewError(connect.CodeCanceled, context.Canceled)
	}
	return stream.Send(&uabv1.RuntimeEvent{
		Kind:       uabv1.RuntimeEventKind_RUNTIME_EVENT_KIND_DONE,
		Status:     "completed",
		UnixMillis: time.Now().UnixMilli(),
	})
}

type textStreamState struct {
	answerStreamed  bool
	thoughtStreamed bool
}

func (state *textStreamState) shouldEmit(partial bool, part *genai.Part) bool {
	if part == nil || part.Text == "" {
		return false
	}
	if partial {
		if part.Thought {
			state.thoughtStreamed = true
		} else {
			state.answerStreamed = true
		}
		return true
	}
	if part.Thought {
		return !state.thoughtStreamed
	}
	return !state.answerStreamed
}

func sendPart(stream *connect.ServerStream[uabv1.RuntimeEvent], messageID string, part *genai.Part, emitText bool) error {
	if part == nil {
		return nil
	}
	now := time.Now().UnixMilli()
	if emitText && part.Text != "" {
		kind := uabv1.RuntimeEventKind_RUNTIME_EVENT_KIND_AGENT_MESSAGE_CHUNK
		if part.Thought {
			kind = uabv1.RuntimeEventKind_RUNTIME_EVENT_KIND_AGENT_THOUGHT_CHUNK
		}
		if err := stream.Send(&uabv1.RuntimeEvent{
			Kind: kind, MessageId: messageID, Text: part.Text, UnixMillis: now,
		}); err != nil {
			return err
		}
	}
	if call := part.FunctionCall; call != nil {
		if err := stream.Send(&uabv1.RuntimeEvent{
			Kind:          uabv1.RuntimeEventKind_RUNTIME_EVENT_KIND_TOOL_CALL,
			MessageId:     messageID,
			ToolCallId:    call.ID,
			ToolName:      call.Name,
			ArgumentsJson: mustJSON(call.Args),
			Status:        "pending",
			UnixMillis:    now,
		}); err != nil {
			return err
		}
	}
	if response := part.FunctionResponse; response != nil {
		return stream.Send(&uabv1.RuntimeEvent{
			Kind:       uabv1.RuntimeEventKind_RUNTIME_EVENT_KIND_TOOL_CALL_UPDATE,
			MessageId:  messageID,
			ToolCallId: response.ID,
			ToolName:   response.Name,
			OutputJson: mustJSON(response.Response),
			Status:     "completed",
			UnixMillis: now,
		})
	}
	return nil
}

func (s *runtimeService) Cancel(_ context.Context, request *connect.Request[uabv1.RuntimeCancelRequest]) (*connect.Response[uabv1.RuntimeCancelResponse], error) {
	if request.Msg.GetSessionId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("session_id is required"))
	}
	return connect.NewResponse(&uabv1.RuntimeCancelResponse{
		Cancelled: s.runs.Cancel(request.Msg.GetSessionId()),
	}), nil
}

func authenticate(token string, next http.Handler) http.Handler {
	expected := []byte("Bearer " + token)
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		credential := request.Header.Get("Authorization")
		if len(credential) > maxBearerBytes || subtle.ConstantTimeCompare([]byte(credential), expected) != 1 {
			response.Header().Set("WWW-Authenticate", `Bearer realm="omai-adk"`)
			http.Error(response, "unauthenticated", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(response, request)
	})
}

func validateIdentity(name, value string) error {
	if value == "" || len(value) > maxIdentityBytes || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must contain between 1 and %d non-padded bytes", name, maxIdentityBytes)
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("%s contains a control character", name)
		}
	}
	return nil
}

func runtimeMessageID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "msg_" + hex.EncodeToString(raw[:]), nil
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func mustJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}
