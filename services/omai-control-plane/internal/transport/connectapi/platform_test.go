package connectapi

import (
	"testing"
	"time"

	platformv1 "github.com/omai/backend/gen/go/omai/platform/v1"
	"github.com/omai/backend/internal/domain"
)

func TestSessionEventV1MapsRuntimeMessageDelta(t *testing.T) {
	t.Parallel()
	event := sessionEventV1(domain.Event{
		Sequence: 7, At: time.UnixMilli(1_750_000_000_000), Type: "acp.session/update",
		SessionID: "session-1", WorkspaceID: "workspace-1", RuntimeID: "go-adk",
		ExternalSessionID: "portal-1",
		PayloadJSON:       []byte(`{"payload":{"update":{"sessionUpdate":"agent_thought_chunk","messageId":"message-1","content":{"text":"checking"}}}}`),
	})
	delta := event.GetMessageDelta()
	if delta == nil || delta.GetMessageId() != "message-1" || delta.GetDelta() != "checking" || delta.GetChannel() != platformv1.MessageChannel_MESSAGE_CHANNEL_REASONING {
		t.Fatalf("unexpected event mapping: %#v", event)
	}
	if event.GetExternalSessionId() != "portal-1" || event.GetSequence() != 7 {
		t.Fatalf("event identity was not preserved: %#v", event)
	}
}

func TestSessionEventV1PreservesUnknownWireEvent(t *testing.T) {
	t.Parallel()
	source := []byte(`{"payload":{"future":true}}`)
	event := sessionEventV1(domain.Event{Type: "future.event", PayloadJSON: source})
	unknown := event.GetUnknown()
	if unknown == nil || unknown.GetWireType() != "future.event" || string(unknown.GetPayloadJson()) != string(source) {
		t.Fatalf("unknown event was not preserved: %#v", event)
	}
}

func TestSessionEventV1MapsInteractionResources(t *testing.T) {
	t.Parallel()
	permission := sessionEventV1(domain.Event{
		At: time.UnixMilli(1_750_000_000_000), Type: "permission.asked", SessionID: "session-1",
		PayloadJSON: []byte(`{"payload":{"id":"permission-1","sessionID":"session-1","permission":"edit","patterns":["src/**"],"metadata":{"reason":"tool"},"always":[]}}`),
	}).GetPermissionRequested().GetPermission()
	if permission.GetId() != "permission-1" || permission.GetPermission() != "edit" || string(permission.GetMetadataJson()) != `{"reason":"tool"}` {
		t.Fatalf("unexpected permission mapping: %#v", permission)
	}
	question := sessionEventV1(domain.Event{
		At: time.UnixMilli(1_750_000_000_000), Type: "question.asked", SessionID: "session-1",
		PayloadJSON: []byte(`{"payload":{"id":"question-1","sessionID":"session-1","questions":[{"question":"Deploy?","header":"Deploy","options":[{"label":"Yes","description":"Now"}],"multiple":false,"custom":false}]}}`),
	}).GetQuestionRequested().GetQuestion()
	if question.GetId() != "question-1" || len(question.GetQuestions()) != 1 || question.GetQuestions()[0].GetHeader() != "Deploy" {
		t.Fatalf("unexpected question mapping: %#v", question)
	}
}
