package gemini

import (
	"encoding/base64"
	"testing"
)

func TestParseAudioTranscriptsInterruptionAndToolCall(t *testing.T) {
	audio := []byte{1, 2, 3, 4}
	event := parse(map[string]any{"serverContent": map[string]any{"turnComplete": true, "interrupted": true, "inputTranscription": map[string]any{"text": "open it"}, "outputTranscription": map[string]any{"text": "done"}, "modelTurn": map[string]any{"parts": []any{map[string]any{"inlineData": map[string]any{"data": base64.StdEncoding.EncodeToString(audio)}}}}}, "toolCall": map[string]any{"functionCalls": []any{map[string]any{"id": "call-1", "name": "list_files", "args": map[string]any{"workspaceId": "w"}}}}, "toolCallCancellation": map[string]any{"ids": []any{"call-2"}}})
	if !event.TurnComplete || !event.Interrupted || len(event.Transcripts) != 2 || event.Transcripts[0].Role != "user" || event.Transcripts[1].Text != "done" {
		t.Fatalf("missing turn metadata: %#v", event)
	}
	if len(event.Audio) != 1 || len(event.Audio[0]) != len(audio) {
		t.Fatalf("audio not parsed: %#v", event.Audio)
	}
	if len(event.Calls) != 1 || event.Calls[0].Name != "list_files" {
		t.Fatalf("tool call not parsed: %#v", event.Calls)
	}
	if len(event.CancelledCallIDs) != 1 || event.CancelledCallIDs[0] != "call-2" {
		t.Fatalf("tool cancellation not parsed: %#v", event.CancelledCallIDs)
	}
}
