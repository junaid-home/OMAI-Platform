package main

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"
)

func TestACPClientRejectsPermissionsAndCollectsAgentText(t *testing.T) {
	t.Parallel()

	serverReader, clientWriter := io.Pipe()
	messages := make(chan acpRead, 4)
	client := &acpClient{stdin: clientWriter, messages: messages}
	serverErrors := make(chan error, 1)
	go func() {
		defer close(messages)
		decoder := json.NewDecoder(serverReader)
		var request rpcMessage
		if err := decoder.Decode(&request); err != nil {
			serverErrors <- err
			return
		}
		messages <- acpRead{message: rpcMessage{
			JSONRPC: "2.0", ID: json.RawMessage(`99`), Method: "session/request_permission",
		}}
		var permissionResponse struct {
			Result struct {
				Outcome struct {
					Outcome  string `json:"outcome"`
					OptionID string `json:"optionId"`
				} `json:"outcome"`
			} `json:"result"`
		}
		if err := decoder.Decode(&permissionResponse); err != nil {
			serverErrors <- err
			return
		}
		if permissionResponse.Result.Outcome.Outcome != "selected" || permissionResponse.Result.Outcome.OptionID != "reject" {
			serverErrors <- io.ErrUnexpectedEOF
			return
		}
		messages <- acpRead{message: rpcMessage{
			JSONRPC: "2.0", Method: "session/update",
			Params: json.RawMessage(`{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"OMAI_DEEPSEEK_ACP_OK"}}}`),
		}}
		messages <- acpRead{message: rpcMessage{
			JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{"stopReason":"end_turn"}`),
		}}
		serverErrors <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var result struct {
		StopReason string `json:"stopReason"`
	}
	if err := client.Request(ctx, "session/prompt", map[string]any{"sessionId": "ses_test"}, &result); err != nil {
		t.Fatal(err)
	}
	if result.StopReason != "end_turn" || client.AgentText() != probeSentinel {
		t.Fatalf("result = %#v, text = %q", result, client.AgentText())
	}
	if err := <-serverErrors; err != nil {
		t.Fatalf("fake ACP server failed: %v", err)
	}
	_ = clientWriter.Close()
	_ = serverReader.Close()
}

func TestLoadProbeConfigRejectsProviderCredentialInProbe(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "must-stay-in-adk")
	if _, err := loadProbeConfig(); err == nil {
		t.Fatal("ACP probe accepted a provider credential in its environment")
	}
}
