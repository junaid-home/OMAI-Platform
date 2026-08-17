package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	uabv1 "github.com/omai/backend/gen/go/uab/v1"
	"github.com/omai/backend/gen/go/uab/v1/uabv1connect"
	"github.com/omai/backend/internal/domain"
)

type fakeModelGateway struct {
	mu      sync.Mutex
	request *uabv1.ModelGenerateRequest
}

func (*fakeModelGateway) Health(context.Context, *connect.Request[uabv1.ModelGatewayHealthRequest]) (*connect.Response[uabv1.ModelGatewayHealthResponse], error) {
	return connect.NewResponse(&uabv1.ModelGatewayHealthResponse{Available: true, Authenticated: true}), nil
}

func (f *fakeModelGateway) Generate(_ context.Context, request *connect.Request[uabv1.ModelGenerateRequest], stream *connect.ServerStream[uabv1.ModelGenerateEvent]) error {
	f.mu.Lock()
	f.request = request.Msg
	f.mu.Unlock()
	for _, event := range []*uabv1.ModelGenerateEvent{
		{Kind: uabv1.ModelGenerateEventKind_MODEL_GENERATE_EVENT_KIND_CONTENT, Partial: true, Content: &uabv1.ModelContent{Role: "model", Parts: []*uabv1.ModelPart{{Text: "hel"}}}},
		{Kind: uabv1.ModelGenerateEventKind_MODEL_GENERATE_EVENT_KIND_CONTENT, Partial: true, Content: &uabv1.ModelContent{Role: "model", Parts: []*uabv1.ModelPart{{Text: "lo"}}}},
		{Kind: uabv1.ModelGenerateEventKind_MODEL_GENERATE_EVENT_KIND_CONTENT, Partial: false, FinishReason: "STOP", Content: &uabv1.ModelContent{Role: "model", Parts: []*uabv1.ModelPart{{Text: "hello"}, {FunctionCall: &uabv1.ModelFunctionCall{Id: "call-1", Name: "read_file", ArgumentsJson: []byte(`{"path":"README.md"}`)}}}}, Usage: &uabv1.ModelUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}},
		{Kind: uabv1.ModelGenerateEventKind_MODEL_GENERATE_EVENT_KIND_DONE},
	} {
		if err := stream.Send(event); err != nil {
			return err
		}
	}
	return nil
}

func TestModelEdgeRoutesChatThroughGoGateway(t *testing.T) {
	fake := &fakeModelGateway{}
	mux := http.NewServeMux()
	path, handler := uabv1connect.NewModelGatewayServiceHandler(fake)
	mux.Handle(path, handler)
	gateway := httptest.NewServer(mux)
	t.Cleanup(gateway.Close)

	leases, err := NewLeaseStore("http://127.0.0.1:8793", time.Hour, 10)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := leases.Issue(domain.Prompt{
		SessionID: "session", ProviderID: "google", ModelID: "gemini-test",
		Principal: domain.Principal{TenantID: "tenant", ActorID: "actor"},
	})
	if err != nil {
		t.Fatal(err)
	}
	edge, err := NewModelEdge(leases, ModelGatewayConfig{Endpoint: gateway.URL, Token: strings.Repeat("g", 32), Transport: "connect"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(edge.Handler())
	t.Cleanup(server.Close)

	payload := map[string]any{
		"model": lease.RouteID,
		"messages": []any{
			map[string]any{"role": "system", "content": "Be precise."},
			map[string]any{"role": "user", "content": "Inspect the repository."},
		},
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{
			"name": "read_file", "description": "Read a file", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
		}}},
	}
	response := postChat(t, server.URL, lease.Token, payload)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected status %d: %s", response.StatusCode, body)
	}
	var result struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content   string         `json:"content"`
				ToolCalls []chatToolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage map[string]int `json:"usage"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if len(result.Choices) != 1 || result.Choices[0].Message.Content != "hello" || result.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("unexpected completion: %#v", result)
	}
	if len(result.Choices[0].Message.ToolCalls) != 1 || result.Choices[0].Message.ToolCalls[0].Function.Name != "read_file" {
		t.Fatalf("tool call was not projected: %#v", result)
	}
	fake.mu.Lock()
	request := fake.request
	fake.mu.Unlock()
	if request.GetTenantId() != "tenant" || request.GetProviderId() != "google" || request.GetModelId() != "gemini-test" {
		t.Fatalf("Go route identity was not preserved: %#v", request)
	}
	if request.GetConfig().GetSystemInstruction() != "Be precise." || len(request.GetTools()) != 1 {
		t.Fatalf("request conversion lost system/tools: %#v", request)
	}
}

func TestModelEdgeStreamsWithoutFinalTextReplay(t *testing.T) {
	fake := &fakeModelGateway{}
	mux := http.NewServeMux()
	path, handler := uabv1connect.NewModelGatewayServiceHandler(fake)
	mux.Handle(path, handler)
	gateway := httptest.NewServer(mux)
	t.Cleanup(gateway.Close)
	leases, _ := NewLeaseStore("http://127.0.0.1:8793", time.Hour, 10)
	lease, _ := leases.Issue(domain.Prompt{SessionID: "session", ProviderID: "google", ModelID: "gemini", Principal: domain.Principal{TenantID: "tenant", ActorID: "actor"}})
	edge, err := NewModelEdge(leases, ModelGatewayConfig{Endpoint: gateway.URL, Token: strings.Repeat("g", 32)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(edge.Handler())
	t.Cleanup(server.Close)
	response := postChat(t, server.URL, lease.Token, map[string]any{
		"model": lease.RouteID, "stream": true, "stream_options": map[string]any{"include_usage": true},
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	})
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("data: [DONE]")) {
		t.Fatalf("unexpected stream %d: %s", response.StatusCode, body)
	}
	if strings.Count(string(body), `"content":"hel"`) != 1 || strings.Count(string(body), `"content":"lo"`) != 1 || strings.Contains(string(body), `"content":"hello"`) {
		t.Fatalf("cumulative final text was replayed: %s", body)
	}
	if !strings.Contains(string(body), `"total_tokens":15`) {
		t.Fatalf("usage missing from stream: %s", body)
	}
}

func TestModelEdgeRejectsWrongRouteAndRemoteImages(t *testing.T) {
	leases, _ := NewLeaseStore("http://127.0.0.1:8793", time.Hour, 10)
	lease, _ := leases.Issue(domain.Prompt{SessionID: "session", ProviderID: "google", ModelID: "gemini", Principal: domain.Principal{TenantID: "tenant", ActorID: "actor"}})
	message := chatRequest{Model: "wrong", Messages: []chatMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}}}
	if _, ok := leases.authorize(lease.Token, message.Model); ok {
		t.Fatal("model lease authorized a foreign route")
	}
	message.Model = lease.RouteID
	message.Messages[0].Content = json.RawMessage(`[{"type":"image_url","image_url":{"url":"https://attacker.example/image.png"}}]`)
	authorization, ok := leases.authorize(lease.Token, lease.RouteID)
	if !ok {
		t.Fatal("route mismatch incorrectly revoked lease")
	}
	if _, err := toModelRequest(message, authorization); err == nil || !strings.Contains(err.Error(), "remote image URLs are forbidden") {
		t.Fatalf("remote image URL was not rejected: %v", err)
	}
}

func TestModelEdgeSanitizesProviderEventErrors(t *testing.T) {
	code, message := safeModelEventError(&uabv1.ModelGenerateEvent{ErrorCode: "UPSTREAM/SECRET", ErrorMessage: "api key sk-secret at https://internal.example"})
	if code != "model_gateway_error" || strings.Contains(message, "secret") || strings.Contains(message, "internal.example") {
		t.Fatalf("provider error leaked through model edge: %q, %q", code, message)
	}
}

func postChat(t *testing.T, baseURL, token string, payload any) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
