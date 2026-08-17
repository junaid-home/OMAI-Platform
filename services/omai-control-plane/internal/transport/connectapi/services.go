package connectapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"connectrpc.com/connect"
	uabv1 "github.com/omai/backend/gen/go/uab/v1"
	"github.com/omai/backend/internal/application"
	"github.com/omai/backend/internal/domain"
	"github.com/omai/backend/internal/platform/auth"
	"github.com/omai/backend/internal/port"
	reflectionregistry "github.com/omai/backend/internal/reflection"
	"google.golang.org/protobuf/types/known/structpb"
)

type Services struct {
	Version       string
	Orchestrator  *application.Orchestrator
	Runtimes      port.RuntimeRegistry
	Sessions      port.SessionRepository
	Conversations port.ConversationRepository
	Events        port.EventRepository
	Workspaces    port.WorkspaceRepository
	Git           port.GitRepository
	Processes     port.ProcessManager
	LSP           port.LSPRegistry
	MCP           port.MCPRepository
	Preview       port.PreviewGateway
	Catalog       *application.Catalog
	Voice         *application.VoiceControl
	Tools         *reflectionregistry.Registry
}

func (s *Services) Health(context.Context, *connect.Request[uabv1.HealthRequest]) (*connect.Response[uabv1.HealthResponse], error) {
	return connect.NewResponse(&uabv1.HealthResponse{Ok: true, UnixMillis: time.Now().UnixMilli(), Version: s.Version}), nil
}

func (s *Services) ListRuntimes(_ context.Context, _ *connect.Request[uabv1.ListRuntimesRequest]) (*connect.Response[uabv1.ListRuntimesResponse], error) {
	response := &uabv1.ListRuntimesResponse{}
	for _, runtimeAdapter := range s.Runtimes.List() {
		d := runtimeAdapter.Descriptor()
		item := &uabv1.RuntimeInstallation{Id: d.ID, Runtime: d.Runtime, Label: d.Label, Version: d.Version, NodeId: d.NodeID, Transport: d.Transport, Enabled: d.Enabled}
		for _, capability := range d.Capabilities {
			item.Capabilities = append(item.Capabilities, &uabv1.Capability{Name: capability.Name, Enabled: capability.Enabled})
		}
		response.Runtimes = append(response.Runtimes, item)
	}
	return connect.NewResponse(response), nil
}

func (s *Services) Prompt(ctx context.Context, request *connect.Request[uabv1.PromptRequest]) (*connect.Response[uabv1.PromptResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	message := request.Msg
	if strings.TrimSpace(message.GetRuntimeId()) == "" || strings.TrimSpace(message.GetExternalSessionId()) == "" || strings.TrimSpace(message.GetRoot()) == "" || strings.TrimSpace(message.GetText()) == "" {
		return nil, connectError(fmt.Errorf("%w: runtime_id, external_session_id, root, and text are required", domain.ErrInvalid))
	}
	model, err := s.Catalog.Resolve(message.GetRuntimeId(), message.GetProviderId(), message.GetModelId())
	if err != nil {
		return nil, connectError(err)
	}
	session, err := s.Orchestrator.Prompt(ctx, principal, domain.Prompt{
		RuntimeID: message.GetRuntimeId(), ExternalSessionID: message.GetExternalSessionId(), Root: message.GetRoot(),
		Text: message.GetText(), Title: message.GetTitle(), ProviderID: message.GetProviderId(), ModelID: message.GetModelId(),
		ModelContextTokens: model.Limits.Context, ModelOutputTokens: model.Limits.Output,
	})
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.PromptResponse{Accepted: true, SessionId: session.ID, WorkspaceId: session.WorkspaceID}), nil
}

func (s *Services) Cancel(ctx context.Context, request *connect.Request[uabv1.CancelRequest]) (*connect.Response[uabv1.CancelResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.Msg.GetRuntimeId()) == "" || strings.TrimSpace(request.Msg.GetExternalSessionId()) == "" {
		return nil, connectError(fmt.Errorf("%w: runtime_id and external_session_id are required", domain.ErrInvalid))
	}
	cancelled := s.Orchestrator.Cancel(ctx, principal, request.Msg.GetRuntimeId(), request.Msg.GetExternalSessionId())
	return connect.NewResponse(&uabv1.CancelResponse{Cancelled: cancelled}), nil
}

func (s *Services) SubscribeEvents(ctx context.Context, request *connect.Request[uabv1.SubscribeEventsRequest], stream *connect.ServerStream[uabv1.Event]) error {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return err
	}
	replay, updates, stop, err := s.Events.Subscribe(ctx, principal, request.Msg.GetSessionId(), request.Msg.GetSince())
	if err != nil {
		return connectError(err)
	}
	defer stop()
	for _, event := range replay {
		if err := stream.Send(eventProto(event)); err != nil {
			return err
		}
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-updates:
			if !ok {
				return connect.NewError(connect.CodeResourceExhausted, errors.New("subscriber fell behind; reconnect with last sequence"))
			}
			if err := stream.Send(eventProto(event)); err != nil {
				return err
			}
		}
	}
}

func (s *Services) Fetch(ctx context.Context, request *connect.Request[uabv1.WorkspaceFetchRequest], stream *connect.ServerStream[uabv1.WorkspaceFetchChunk]) error {
	headers := make(map[string]string)
	for _, header := range request.Msg.GetHeaders() {
		headers[header.GetName()] = header.GetValue()
	}
	response, err := s.Preview.Fetch(ctx, request.Msg.GetMethod(), request.Msg.GetPath(), headers, bytesReader(request.Msg.GetBody()))
	if err != nil {
		return connectError(err)
	}
	defer response.Body.Close()
	if response.Status < 100 || response.Status > 599 {
		return connect.NewError(connect.CodeUnavailable, errors.New("preview upstream returned an invalid HTTP status"))
	}
	first := &uabv1.WorkspaceFetchChunk{Status: int32(response.Status)}
	for name, values := range response.Header {
		for _, value := range values {
			first.Headers = append(first.Headers, &uabv1.Header{Name: name, Value: value})
		}
	}
	if err := stream.Send(first); err != nil {
		return err
	}
	buffer := make([]byte, 32*1024)
	for {
		read, readErr := response.Body.Read(buffer)
		if read > 0 {
			if err := stream.Send(&uabv1.WorkspaceFetchChunk{Data: append([]byte(nil), buffer[:read]...)}); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return stream.Send(&uabv1.WorkspaceFetchChunk{End: true})
		}
		if readErr != nil {
			return stream.Send(&uabv1.WorkspaceFetchChunk{End: true, Error: readErr.Error()})
		}
	}
}

func (s *Services) ListTools(context.Context, *connect.Request[uabv1.ListToolsRequest]) (*connect.Response[uabv1.ListToolsResponse], error) {
	tools, etag := s.Tools.List()
	return connect.NewResponse(&uabv1.ListToolsResponse{Tools: tools, RegistryEtag: etag}), nil
}

func (s *Services) GetCatalog(context.Context, *connect.Request[structpb.Struct]) (*connect.Response[structpb.Struct], error) {
	return s.catalogResponse("", "", "", 0, 10000)
}
func (s *Services) ListProviders(_ context.Context, request *connect.Request[structpb.Struct]) (*connect.Response[structpb.Struct], error) {
	providers := s.Catalog.SearchProviders(field(request.Msg, "query"), field(request.Msg, "runtime_id"), boolField(request.Msg, "connected_only"), intField(request.Msg, "limit", 100))
	value, err := newStruct(map[string]any{"schema_version": "1", "providers": providers, "connected": connectedProviderIDs(providers), "default": s.Catalog.DefaultSnapshot()})
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(value), nil
}
func (s *Services) ListModels(_ context.Context, request *connect.Request[structpb.Struct]) (*connect.Response[structpb.Struct], error) {
	return s.catalogResponse("", field(request.Msg, "runtime_id"), field(request.Msg, "provider_id"), intField(request.Msg, "offset", 0), intField(request.Msg, "limit", 100))
}
func (s *Services) SearchModels(_ context.Context, request *connect.Request[structpb.Struct]) (*connect.Response[structpb.Struct], error) {
	return s.catalogResponse(field(request.Msg, "query"), field(request.Msg, "runtime_id"), field(request.Msg, "provider_id"), intField(request.Msg, "offset", 0), intField(request.Msg, "limit", 100))
}
func (s *Services) GetModel(_ context.Context, request *connect.Request[structpb.Struct]) (*connect.Response[structpb.Struct], error) {
	id := field(request.Msg, "id")
	providerID := field(request.Msg, "provider_id")
	model, err := s.Catalog.GetModel(providerID, id)
	if err != nil {
		return nil, connectError(err)
	}
	value, err := newStruct(map[string]any{"schema_version": "1", "model": model})
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(value), nil
}

func (s *Services) catalogResponse(query, runtimeID, providerID string, offset, limit int) (*connect.Response[structpb.Struct], error) {
	page := s.Catalog.SearchPage(query, runtimeID, providerID, offset, limit)
	providers := s.Catalog.SearchProviders("", runtimeID, false, 1000)
	value, err := newStruct(map[string]any{"schema_version": "1", "providers": providers, "connected": connectedProviderIDs(providers), "default": s.Catalog.DefaultSnapshot(), "models": page.Models, "total": page.Total, "offset": page.Offset, "next_offset": page.NextOffset})
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(value), nil
}

func eventProto(event domain.Event) *uabv1.Event {
	return &uabv1.Event{Seq: event.Sequence, UnixMillis: event.At.UnixMilli(), Type: event.Type, WorkspaceId: event.WorkspaceID, SessionId: event.SessionID, RuntimeId: event.RuntimeID, ExternalSessionId: event.ExternalSessionID, PayloadJson: append([]byte(nil), event.PayloadJSON...)}
}
func requirePrincipal(ctx context.Context) (domain.Principal, error) {
	principal, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return domain.Principal{}, connect.NewError(connect.CodeUnauthenticated, errors.New("principal missing"))
	}
	return principal, nil
}
func connectError(err error) error {
	code := connect.CodeInternal
	switch {
	case errors.Is(err, domain.ErrInvalid):
		code = connect.CodeInvalidArgument
	case errors.Is(err, domain.ErrNotFound):
		code = connect.CodeNotFound
	case errors.Is(err, domain.ErrStaleRevision):
		code = connect.CodeAborted
	case errors.Is(err, domain.ErrConflict):
		code = connect.CodeAlreadyExists
	case errors.Is(err, domain.ErrForbidden):
		code = connect.CodePermissionDenied
	case errors.Is(err, domain.ErrUnavailable):
		code = connect.CodeUnavailable
	case errors.Is(err, domain.ErrReplayTooOld):
		code = connect.CodeOutOfRange
	}
	return connect.NewError(code, err)
}

type reader struct{ data []byte }

func bytesReader(data []byte) io.Reader { return &reader{data: data} }
func (r *reader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}
func field(value *structpb.Struct, name string) string {
	if value == nil {
		return ""
	}
	return value.GetFields()[name].GetStringValue()
}
func intField(value *structpb.Struct, name string, fallback int) int {
	if value == nil || value.GetFields()[name] == nil {
		return fallback
	}
	return int(value.GetFields()[name].GetNumberValue())
}
func boolField(value *structpb.Struct, name string) bool {
	return value != nil && value.GetFields()[name].GetBoolValue()
}
func connectedProviderIDs(providers []domain.Provider) []string {
	result := make([]string, 0, len(providers))
	for _, provider := range providers {
		if provider.Connected {
			result = append(result, provider.ID)
		}
	}
	return result
}
func newStruct(value any) (*structpb.Struct, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, err
	}
	return structpb.NewStruct(object)
}
