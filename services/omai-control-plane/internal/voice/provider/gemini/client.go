package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/omai/backend/internal/voice/protocol"
	"github.com/omai/backend/internal/voice/provider"
)

type Factory struct{ Endpoint, APIKey, Model, SystemPrompt string }
type client struct {
	conn   *websocket.Conn
	mu     sync.Mutex
	events chan provider.Event
	model  string
}

func (f *Factory) Connect(ctx context.Context, tools []provider.Tool, voice string) (provider.Session, error) {
	u, err := url.Parse(f.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse Gemini endpoint: %w", err)
	}
	query := u.Query()
	query.Set("key", f.APIKey)
	u.RawQuery = query.Encode()
	connection, response, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPHeader: http.Header{"User-Agent": {"OMAI-Voice-Go/1.0"}}})
	if err != nil {
		if response != nil {
			return nil, fmt.Errorf("gemini websocket status %d: %w", response.StatusCode, err)
		}
		return nil, err
	}
	connection.SetReadLimit(10 << 20)
	declarations := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		declarations = append(declarations, map[string]any{"name": tool.Name, "description": tool.Description, "parameters": tool.Parameters})
	}
	setupBody := map[string]any{"model": "models/" + f.Model, "generationConfig": map[string]any{"responseModalities": []string{"AUDIO"}, "speechConfig": map[string]any{"voiceConfig": map[string]any{"prebuiltVoiceConfig": map[string]any{"voiceName": voice}}}}, "systemInstruction": map[string]any{"parts": []map[string]string{{"text": f.SystemPrompt}}}, "inputAudioTranscription": map[string]any{}, "outputAudioTranscription": map[string]any{}, "contextWindowCompression": map[string]any{"slidingWindow": map[string]any{}}}
	if len(declarations) > 0 {
		setupBody["tools"] = []map[string]any{{"functionDeclarations": declarations}}
	}
	setup := map[string]any{"setup": setupBody}
	if err := wsjson.Write(ctx, connection, setup); err != nil {
		_ = connection.Close(websocket.StatusInternalError, "setup failed")
		return nil, err
	}
	readyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var ready map[string]any
	if err := wsjson.Read(readyCtx, connection, &ready); err != nil {
		_ = connection.Close(websocket.StatusInternalError, "setup failed")
		return nil, err
	}
	if _, ok := ready["setupComplete"]; !ok {
		_ = connection.Close(websocket.StatusPolicyViolation, "invalid setup response")
		return nil, fmt.Errorf("gemini setup was not acknowledged")
	}
	result := &client{conn: connection, events: make(chan provider.Event, 64), model: f.Model}
	go result.readLoop(ctx)
	return result, nil
}
func (c *client) Name() string                  { return "gemini-live" }
func (c *client) Model() string                 { return c.model }
func (c *client) InputSampleRate() uint32       { return 16000 }
func (c *client) OutputSampleRate() uint32      { return 24000 }
func (c *client) Events() <-chan provider.Event { return c.events }
func (c *client) SendAudio(ctx context.Context, audio []byte) error {
	return c.write(ctx, map[string]any{"realtimeInput": map[string]any{"audio": map[string]any{"mimeType": "audio/pcm;rate=16000", "data": base64.StdEncoding.EncodeToString(audio)}}})
}
func (c *client) SendToolResult(ctx context.Context, result protocol.ToolResult) error {
	return c.write(ctx, map[string]any{"toolResponse": map[string]any{"functionResponses": []map[string]any{{"id": result.ID, "name": result.Name, "response": result.Response}}}})
}
func (c *client) Interrupt(ctx context.Context) error {
	return c.write(ctx, map[string]any{"realtimeInput": map[string]any{"audioStreamEnd": true}})
}
func (c *client) Close() error { return c.conn.Close(websocket.StatusNormalClosure, "session closed") }
func (c *client) write(ctx context.Context, value any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return wsjson.Write(ctx, c.conn, value)
}
func (c *client) readLoop(ctx context.Context) {
	defer close(c.events)
	for {
		var raw map[string]any
		if err := wsjson.Read(ctx, c.conn, &raw); err != nil {
			select {
			case c.events <- provider.Event{Err: err}:
			case <-ctx.Done():
			}
			return
		}
		event := parse(raw)
		if len(event.Audio) > 0 || len(event.Transcripts) > 0 || event.TurnComplete || event.Interrupted || len(event.Calls) > 0 || len(event.CancelledCallIDs) > 0 {
			select {
			case c.events <- event:
			case <-ctx.Done():
				return
			}
		}
	}
}
func parse(raw map[string]any) provider.Event {
	var event provider.Event
	if content, ok := raw["serverContent"].(map[string]any); ok {
		event.TurnComplete, _ = content["turnComplete"].(bool)
		event.Interrupted, _ = content["interrupted"].(bool)
		if transcription, ok := content["inputTranscription"].(map[string]any); ok {
			if text, _ := transcription["text"].(string); text != "" {
				event.Transcripts = append(event.Transcripts, provider.Transcript{Role: "user", Text: text})
			}
		}
		if transcription, ok := content["outputTranscription"].(map[string]any); ok {
			if text, _ := transcription["text"].(string); text != "" {
				event.Transcripts = append(event.Transcripts, provider.Transcript{Role: "assistant", Text: text})
			}
		}
		if turn, ok := content["modelTurn"].(map[string]any); ok {
			if parts, ok := turn["parts"].([]any); ok {
				for _, value := range parts {
					part, _ := value.(map[string]any)
					inline, _ := part["inlineData"].(map[string]any)
					encoded, _ := inline["data"].(string)
					if audio, err := base64.StdEncoding.DecodeString(encoded); err == nil && len(audio) > 0 {
						event.Audio = append(event.Audio, audio)
					}
				}
			}
		}
	}
	if callBlock, ok := raw["toolCall"].(map[string]any); ok {
		if calls, ok := callBlock["functionCalls"].([]any); ok {
			for _, value := range calls {
				encoded, _ := json.Marshal(value)
				var call protocol.ToolCall
				if json.Unmarshal(encoded, &call) == nil && call.Name != "" {
					event.Calls = append(event.Calls, call)
				}
			}
		}
	}
	if cancellation, ok := raw["toolCallCancellation"].(map[string]any); ok {
		if ids, ok := cancellation["ids"].([]any); ok {
			for _, value := range ids {
				if id, ok := value.(string); ok && validProviderID(id) {
					event.CancelledCallIDs = append(event.CancelledCallIDs, id)
				}
			}
		}
	}
	return event
}
func validProviderID(value string) bool { return value != "" && len(value) <= 256 }
