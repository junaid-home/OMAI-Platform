package runtimeapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	uabv1 "github.com/omai/backend/gen/go/uab/v1"
	"github.com/omai/backend/gen/go/uab/v1/uabv1connect"
	"github.com/omai/backend/internal/domain"
)

type fakeRuntime struct {
	prompt domain.Prompt
}

func (*fakeRuntime) Descriptor() domain.RuntimeDescriptor {
	return domain.RuntimeDescriptor{ID: "opencode", Runtime: "opencode", Version: "test", Enabled: true}
}
func (*fakeRuntime) Health(context.Context) domain.RuntimeHealth {
	return domain.RuntimeHealth{RuntimeID: "opencode", Available: true, Authenticated: true, Version: "test"}
}
func (r *fakeRuntime) Run(_ context.Context, prompt domain.Prompt, emit func(domain.RuntimeEvent) error) error {
	r.prompt = prompt
	return emit(domain.RuntimeEvent{Kind: domain.RuntimeEventAgentMessage, MessageID: "message", Text: "hello", At: time.Now()})
}
func (*fakeRuntime) Cancel(context.Context, string) bool { return true }

func TestRuntimeServicePinsTenantWorkspaceAndStreams(t *testing.T) {
	runtime := &fakeRuntime{}
	service := &Service{Runtime: runtime, AllowedTenant: "tenant", ExpectedWorkspaceID: "workspace", Root: "/workspace"}
	mux := http.NewServeMux()
	path, handler := uabv1connect.NewAgentRuntimeServiceHandler(service)
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := uabv1connect.NewAgentRuntimeServiceClient(http.DefaultClient, server.URL)
	request := &uabv1.RuntimePrompt{
		TenantId: "tenant", ActorId: "actor", SessionId: "session", ExternalSessionId: "external",
		WorkspaceId: "workspace", Root: "/control-plane/workspace", Text: "prompt", ProviderId: "google", ModelId: "gemini",
		ModelContextTokens: 1_000_000, ModelOutputTokens: 64_000,
	}
	stream, err := client.Run(context.Background(), connect.NewRequest(request))
	if err != nil {
		t.Fatal(err)
	}
	if !stream.Receive() || stream.Msg().GetText() != "hello" {
		t.Fatalf("runtime event was not streamed: %v", stream.Err())
	}
	if runtime.prompt.Principal.TenantID != "tenant" || runtime.prompt.WorkspaceID != "workspace" || runtime.prompt.Root != "/workspace" || runtime.prompt.ModelContextTokens != 1_000_000 || runtime.prompt.ModelOutputTokens != 64_000 {
		t.Fatalf("identity was not preserved: %#v", runtime.prompt)
	}

	foreign := &uabv1.RuntimePrompt{
		TenantId: "foreign", ActorId: "actor", SessionId: "session", ExternalSessionId: "external",
		WorkspaceId: "workspace", Root: "/control-plane/workspace", Text: "prompt", ProviderId: "google", ModelId: "gemini",
	}
	stream, err = client.Run(context.Background(), connect.NewRequest(foreign))
	if err != nil {
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("unexpected foreign tenant error: %v", err)
		}
		return
	}
	if stream.Receive() || connect.CodeOf(stream.Err()) != connect.CodePermissionDenied {
		t.Fatalf("foreign tenant was not rejected: %v", stream.Err())
	}
}
