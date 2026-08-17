package harness

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	uabv1 "github.com/omai/backend/gen/go/uab/v1"
	"github.com/omai/backend/gen/go/uab/v1/uabv1connect"
	"github.com/omai/backend/internal/domain"
)

// TestOpenCodeSourceEndToEnd deliberately runs the real OpenCode TypeScript
// entrypoint when its paths are supplied by the caller. It stays opt-in so the
// Go backend test suite does not acquire a Bun/Node dependency.
func TestOpenCodeSourceEndToEnd(t *testing.T) {
	command := os.Getenv("OMAI_TEST_OPENCODE_COMMAND")
	entry := os.Getenv("OMAI_TEST_OPENCODE_ENTRY")
	if command == "" || entry == "" {
		t.Skip("set OMAI_TEST_OPENCODE_COMMAND and OMAI_TEST_OPENCODE_ENTRY to run the real harness")
	}
	if !filepath.IsAbs(command) || !filepath.IsAbs(entry) {
		t.Fatal("real harness test paths must be absolute")
	}

	workspace := t.TempDir()
	targetFile := filepath.Join(workspace, "harness-e2e.txt")
	forbiddenFile := filepath.Join(t.TempDir(), "must-not-be-written.txt")
	gatewayService := &finalModelGateway{targetFile: targetFile, forbiddenFile: forbiddenFile}
	gatewayMux := http.NewServeMux()
	path, handler := uabv1connect.NewModelGatewayServiceHandler(gatewayService)
	gatewayMux.Handle(path, handler)
	gateway := httptest.NewServer(gatewayMux)
	t.Cleanup(gateway.Close)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	edgeBaseURL := "http://" + listener.Addr().String()
	leases, err := NewLeaseStore(edgeBaseURL, time.Minute, 8)
	if err != nil {
		t.Fatal(err)
	}
	edge, err := NewModelEdge(leases, ModelGatewayConfig{
		Endpoint:  gateway.URL,
		Token:     strings.Repeat("g", 32),
		Transport: "connect",
	}, "e2e")
	if err != nil {
		t.Fatal(err)
	}
	edgeServer := &http.Server{Handler: edge.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		_ = edgeServer.Serve(listener)
	}()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = edgeServer.Shutdown(shutdownCtx)
	})

	driver, err := NewOpenCode(OpenCodeConfig{
		ID:          "opencode-source-e2e",
		Command:     command,
		CommandArgs: []string{"--conditions=browser", entry},
		Workspace:   workspace,
		Home:        filepath.Join(t.TempDir(), "home"),
		Version:     "source-e2e",
		AutoApprove: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := NewFileSessionStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewSupervisor(driver, NewLocalRunner(8<<20, 1<<20), sessions, leases)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var events []domain.RuntimeEvent
	err = runtime.Run(ctx, domain.Prompt{
		SessionID:         "omai-run-e2e",
		ExternalSessionID: "omai-thread-e2e",
		WorkspaceID:       "workspace-e2e",
		Root:              workspace,
		Text:              "Answer with the exact text requested by the model.",
		Title:             "OMAI harness end-to-end",
		ProviderID:        "google",
		ModelID:           "gemini-e2e",
		Principal:         domain.Principal{TenantID: "tenant-e2e", ActorID: "actor-e2e"},
	}, func(event domain.RuntimeEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var hasText, hasDeniedTool, hasTool, hasDone bool
	for _, event := range events {
		hasText = hasText || (event.Kind == domain.RuntimeEventAgentMessage && strings.Contains(event.Text, "OMAI harness e2e"))
		hasDeniedTool = hasDeniedTool || (event.Kind == domain.RuntimeEventToolUpdate && event.ToolName == "write" && event.Status == "error")
		hasTool = hasTool || (event.Kind == domain.RuntimeEventToolUpdate && event.ToolName == "write" && event.Status == "completed")
		hasDone = hasDone || event.Kind == domain.RuntimeEventDone
	}
	if !hasText || !hasDeniedTool || !hasTool || !hasDone {
		t.Fatalf("real OpenCode event stream was incomplete: %#v", events)
	}
	contents, err := os.ReadFile(targetFile)
	if err != nil || string(contents) != "workspace mutation owned by the OMAI harness\n" {
		t.Fatalf("real OpenCode tool did not mutate the assigned workspace: %q, %v", contents, err)
	}
	if _, err := os.Stat(forbiddenFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenCode escaped the assigned workspace: %v", err)
	}
	nativeSessionID, ok := sessions.Get("omai-thread-e2e")
	if !ok || !strings.HasPrefix(nativeSessionID, "ses_") {
		t.Fatalf("native OpenCode session was not persisted: %q, %t", nativeSessionID, ok)
	}
	var resumed []domain.RuntimeEvent
	err = runtime.Run(ctx, domain.Prompt{
		SessionID:         "omai-run-e2e-resumed",
		ExternalSessionID: "omai-thread-e2e",
		WorkspaceID:       "workspace-e2e",
		Root:              workspace,
		Text:              "Continue this same native coding session.",
		ProviderID:        "google",
		ModelID:           "gemini-e2e",
		Principal:         domain.Principal{TenantID: "tenant-e2e", ActorID: "actor-e2e"},
	}, func(event domain.RuntimeEvent) error {
		resumed = append(resumed, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if current, exists := sessions.Get("omai-thread-e2e"); !exists || current != nativeSessionID {
		t.Fatalf("OpenCode resume changed the native session: %q != %q", current, nativeSessionID)
	}
	if len(resumed) < 3 || resumed[len(resumed)-1].Kind != domain.RuntimeEventDone {
		t.Fatalf("resumed OpenCode stream was incomplete: %#v", resumed)
	}

	gatewayService.mu.Lock()
	request := gatewayService.request
	calls := gatewayService.calls
	gatewayService.mu.Unlock()
	if calls != 4 || request == nil || request.GetTenantId() != "tenant-e2e" || request.GetActorId() != "actor-e2e" || request.GetSessionId() != "omai-run-e2e-resumed" || request.GetProviderId() != "google" || request.GetModelId() != "gemini-e2e" {
		t.Fatalf("Go-owned model route was not preserved: %#v", request)
	}
	if !containsFunctionResult(request, "write") {
		t.Fatalf("OpenCode tool result was not routed back through Go: %#v", request.GetContents())
	}
}

type finalModelGateway struct {
	mu            sync.Mutex
	request       *uabv1.ModelGenerateRequest
	calls         int
	targetFile    string
	forbiddenFile string
}

func (*finalModelGateway) Health(context.Context, *connect.Request[uabv1.ModelGatewayHealthRequest]) (*connect.Response[uabv1.ModelGatewayHealthResponse], error) {
	return connect.NewResponse(&uabv1.ModelGatewayHealthResponse{Available: true, Authenticated: true}), nil
}

func (f *finalModelGateway) Generate(_ context.Context, request *connect.Request[uabv1.ModelGenerateRequest], stream *connect.ServerStream[uabv1.ModelGenerateEvent]) error {
	f.mu.Lock()
	f.request = request.Msg
	f.calls++
	call := f.calls
	targetFile := f.targetFile
	forbiddenFile := f.forbiddenFile
	f.mu.Unlock()
	if call == 1 {
		if !containsTool(request.Msg, "write") {
			return connect.NewError(connect.CodeFailedPrecondition, nil)
		}
		return sendWriteCall(stream, "call_e2e_forbidden", forbiddenFile, "escape attempt\n")
	}
	if call == 2 {
		return sendWriteCall(stream, "call_e2e_write", targetFile, "workspace mutation owned by the OMAI harness\n")
	}
	if err := stream.Send(&uabv1.ModelGenerateEvent{
		Kind:         uabv1.ModelGenerateEventKind_MODEL_GENERATE_EVENT_KIND_CONTENT,
		Partial:      false,
		FinishReason: "STOP",
		Content:      &uabv1.ModelContent{Role: "model", Parts: []*uabv1.ModelPart{{Text: "OMAI harness e2e"}}},
		Usage:        &uabv1.ModelUsage{InputTokens: 7, OutputTokens: 4, TotalTokens: 11},
	}); err != nil {
		return err
	}
	return stream.Send(&uabv1.ModelGenerateEvent{Kind: uabv1.ModelGenerateEventKind_MODEL_GENERATE_EVENT_KIND_DONE})
}

func sendWriteCall(stream *connect.ServerStream[uabv1.ModelGenerateEvent], id, path, content string) error {
	if err := stream.Send(&uabv1.ModelGenerateEvent{
		Kind:         uabv1.ModelGenerateEventKind_MODEL_GENERATE_EVENT_KIND_CONTENT,
		Partial:      false,
		FinishReason: "TOOL_USE",
		Content: &uabv1.ModelContent{Role: "model", Parts: []*uabv1.ModelPart{{FunctionCall: &uabv1.ModelFunctionCall{
			Id: id, Name: "write", ArgumentsJson: []byte(`{"filePath":` + mustJSON(path) + `,"content":` + mustJSON(content) + `}`),
		}}}},
	}); err != nil {
		return err
	}
	return stream.Send(&uabv1.ModelGenerateEvent{Kind: uabv1.ModelGenerateEventKind_MODEL_GENERATE_EVENT_KIND_DONE})
}

func containsTool(request *uabv1.ModelGenerateRequest, name string) bool {
	for _, tool := range request.GetTools() {
		if tool.GetName() == name {
			return true
		}
	}
	return false
}

func containsFunctionResult(request *uabv1.ModelGenerateRequest, name string) bool {
	for _, content := range request.GetContents() {
		for _, part := range content.GetParts() {
			if part.GetFunctionResult().GetName() == name {
				return true
			}
		}
	}
	return false
}

func mustJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
