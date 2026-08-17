package harness

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/omai/backend/internal/domain"
)

func TestOpenCodeInvocationUsesStdinAndGoModelCapability(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-reach-harness")
	workspace := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	driver, err := NewOpenCode(OpenCodeConfig{
		ID: "opencode-reference", Command: "opencode", Workspace: workspace,
		Home: home, Version: "test", AutoApprove: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	prompt := domain.Prompt{
		SessionID: "session-1", ExternalSessionID: "portal-1", WorkspaceID: "workspace-1",
		Root: workspace, Text: "fix the test", Title: "Fix", ProviderID: "google", ModelID: "gemini-test",
		ModelContextTokens: 1_000_000, ModelOutputTokens: 64_000,
	}
	invocation, err := driver.Invocation(prompt, "ses_open_code", ModelLease{
		Token: strings.Repeat("a", 43), RouteID: "route-test", BaseURL: "http://127.0.0.1:8793/v1", Expiry: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(invocation.Stdin) != "fix the test\n" || invocation.Dir != workspace {
		t.Fatalf("unexpected invocation: %#v", invocation)
	}
	joined := strings.Join(invocation.Args, "\x00")
	for _, expected := range []string{"run", "--format", "json", "--auto", "--session", "ses_open_code", "omai/route-test"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("missing argument %q in %#v", expected, invocation.Args)
		}
	}
	if slices.ContainsFunc(invocation.Env, func(value string) bool { return strings.Contains(value, "must-not-reach-harness") }) {
		t.Fatal("provider secret leaked into harness environment")
	}
	configuration := environmentValue(invocation.Env, "OPENCODE_CONFIG_CONTENT")
	var document map[string]any
	if err := json.Unmarshal([]byte(configuration), &document); err != nil {
		t.Fatal(err)
	}
	provider := document["provider"].(map[string]any)["omai"].(map[string]any)
	if document["model"] != "omai/route-test" {
		t.Fatalf("default harness model was not capability scoped: %#v", document["model"])
	}
	permissions := document["permission"].(map[string]any)
	if permissions["external_directory"] != "deny" {
		t.Fatalf("external workspace access was not denied: %#v", permissions)
	}
	options := provider["options"].(map[string]any)
	if options["apiKey"] != strings.Repeat("a", 43) || options["baseURL"] != "http://127.0.0.1:8793/v1" {
		t.Fatalf("model gateway configuration was not capability scoped: %#v", options)
	}
	model := provider["models"].(map[string]any)["route-test"].(map[string]any)
	limits := model["limit"].(map[string]any)
	if limits["context"] != float64(1_000_000) || limits["output"] != float64(64_000) {
		t.Fatalf("models.dev limits were not preserved: %#v", limits)
	}
	if environmentValue(invocation.Env, "OPENCODE_DISABLE_PROJECT_CONFIG") != "true" {
		t.Fatal("untrusted project configuration was not disabled")
	}
}

func TestOpenCodeDecoderNormalizesTextToolsAndErrors(t *testing.T) {
	decoder := (&OpenCode{}).NewDecoder()
	text, err := decoder.Decode([]byte(`{"type":"text","timestamp":1700000000000,"sessionID":"ses_1","part":{"id":"part_1","type":"text","text":"done"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if text.HarnessSessionID != "ses_1" || len(text.Events) != 1 || text.Events[0].Kind != domain.RuntimeEventAgentMessage || text.Events[0].Text != "done" {
		t.Fatalf("unexpected text event: %#v", text)
	}
	tool, err := decoder.Decode([]byte(`{"type":"tool_use","timestamp":1700000000001,"sessionID":"ses_1","part":{"id":"call_1","type":"tool","tool":"bash","state":{"status":"completed","input":{"command":"pwd"},"output":"ok"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(tool.Events) != 2 || tool.Events[0].Kind != domain.RuntimeEventToolCall || tool.Events[1].Kind != domain.RuntimeEventToolUpdate {
		t.Fatalf("unexpected tool events: %#v", tool.Events)
	}
	if string(tool.Events[0].ArgumentsJSON) != `{"command":"pwd"}` || string(tool.Events[1].OutputJSON) != `"ok"` {
		t.Fatalf("unexpected tool payloads: %#v", tool.Events)
	}
	failure, err := decoder.Decode([]byte(`{"type":"error","timestamp":1700000000002,"sessionID":"ses_1","error":{"name":"ProviderError","data":{"message":"route unavailable"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(failure.Events) != 1 || failure.Events[0].Kind != domain.RuntimeEventError || failure.Events[0].Text != "route unavailable" {
		t.Fatalf("unexpected error event: %#v", failure)
	}
}

func TestOpenCodeDecoderToleratesVersionedEnvelopeFields(t *testing.T) {
	decoded, err := (&OpenCode{}).NewDecoder().Decode([]byte(`{"type":"text","timestamp":1700000000000,"sessionID":"ses_1","protocolVersion":"future","part":{"id":"part_1","type":"text","text":"done","metadata":{"future":true}}}`))
	if err != nil || len(decoded.Events) != 1 || decoded.Events[0].Text != "done" {
		t.Fatalf("forward-compatible event was rejected: %#v, %v", decoded, err)
	}
}

func TestOpenCodeRejectsForeignWorkspace(t *testing.T) {
	workspace := t.TempDir()
	driver, err := NewOpenCode(OpenCodeConfig{Command: "opencode", Workspace: workspace, Home: filepath.Join(t.TempDir(), "home")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = driver.Invocation(domain.Prompt{Root: t.TempDir(), WorkspaceID: "foreign", Text: "x"}, "", ModelLease{Token: "x", RouteID: "x", BaseURL: "x"})
	if err == nil {
		t.Fatal("foreign workspace was accepted")
	}
}

func environmentValue(environment []string, name string) string {
	prefix := name + "="
	for _, value := range environment {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimPrefix(value, prefix)
		}
	}
	return ""
}
