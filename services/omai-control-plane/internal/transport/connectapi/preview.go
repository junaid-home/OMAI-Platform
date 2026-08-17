package connectapi

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	uabv1 "github.com/omai/backend/gen/go/uab/v1"
	"github.com/omai/backend/internal/application"
	"github.com/omai/backend/internal/domain"
)

type PreviewService struct{ Manager *application.PreviewManager }

func (s *PreviewService) Analyze(ctx context.Context, request *connect.Request[uabv1.PreviewAnalyzeRequest]) (*connect.Response[uabv1.PreviewAnalyzeResponse], error) {
	principal, root, err := previewRoot(ctx, request.Msg.GetRoot())
	if err != nil {
		return nil, err
	}
	plan, err := s.Manager.Analyze(ctx, principal, root)
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.PreviewAnalyzeResponse{Plan: previewPlanProto(plan)}), nil
}

func (s *PreviewService) Start(ctx context.Context, request *connect.Request[uabv1.PreviewStartRequest]) (*connect.Response[uabv1.PreviewStartResponse], error) {
	principal, root, err := previewRoot(ctx, request.Msg.GetRoot())
	if err != nil {
		return nil, err
	}
	instance, err := s.Manager.Start(ctx, principal, root, false)
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.PreviewStartResponse{Preview: previewInstanceProto(instance)}), nil
}

func (s *PreviewService) Restart(ctx context.Context, request *connect.Request[uabv1.PreviewRestartRequest]) (*connect.Response[uabv1.PreviewRestartResponse], error) {
	principal, root, err := previewRoot(ctx, request.Msg.GetRoot())
	if err != nil {
		return nil, err
	}
	instance, err := s.Manager.Start(ctx, principal, root, true)
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.PreviewRestartResponse{Preview: previewInstanceProto(instance)}), nil
}

func (s *PreviewService) Get(ctx context.Context, request *connect.Request[uabv1.PreviewGetRequest]) (*connect.Response[uabv1.PreviewGetResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	workspaceID := strings.TrimSpace(request.Msg.GetWorkspaceId())
	if workspaceID == "" || len(workspaceID) > 256 {
		return nil, connectError(domain.ErrInvalid)
	}
	instance, err := s.Manager.Get(ctx, principal, workspaceID)
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.PreviewGetResponse{Preview: previewInstanceProto(instance)}), nil
}

func (s *PreviewService) Stop(ctx context.Context, request *connect.Request[uabv1.PreviewStopRequest]) (*connect.Response[uabv1.PreviewStopResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	workspaceID := strings.TrimSpace(request.Msg.GetWorkspaceId())
	if workspaceID == "" || len(workspaceID) > 256 {
		return nil, connectError(domain.ErrInvalid)
	}
	if err := s.Manager.Stop(ctx, principal, workspaceID); err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.PreviewStopResponse{Stopped: true}), nil
}

func (s *PreviewService) WatchLogs(ctx context.Context, request *connect.Request[uabv1.PreviewWatchLogsRequest], stream *connect.ServerStream[uabv1.PreviewLogChunk]) error {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return err
	}
	workspaceID := strings.TrimSpace(request.Msg.GetWorkspaceId())
	if workspaceID == "" || len(workspaceID) > 256 {
		return connectError(domain.ErrInvalid)
	}
	replay, updates, stop, err := s.Manager.Watch(ctx, principal, workspaceID, request.Msg.GetCursor())
	if err != nil {
		return connectError(err)
	}
	defer stop()
	exited := false
	for _, chunk := range replay {
		exited = exited || chunk.Exited
		if err := stream.Send(previewChunkProto(chunk)); err != nil {
			return err
		}
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case chunk, ok := <-updates:
			if !ok {
				if exited {
					return nil
				}
				return connect.NewError(connect.CodeResourceExhausted, errors.New("preview log subscriber fell behind; reconnect with last cursor"))
			}
			exited = exited || chunk.Exited
			if err := stream.Send(previewChunkProto(chunk)); err != nil {
				return err
			}
		}
	}
}

func previewRoot(ctx context.Context, value string) (domain.Principal, string, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return domain.Principal{}, "", err
	}
	root := strings.TrimSpace(value)
	if root == "" || len(root) > 16<<10 || strings.ContainsRune(root, '\x00') {
		return domain.Principal{}, "", connectError(domain.ErrInvalid)
	}
	return principal, root, nil
}

func previewPlanProto(value domain.RuntimePlan) *uabv1.PreviewRuntimePlan {
	result := &uabv1.PreviewRuntimePlan{
		Version: value.Version, WorkspaceId: value.WorkspaceID, Fingerprint: value.Fingerprint, Source: value.Source,
		Primary: value.Primary, AnalyzedUnixMillis: value.AnalyzedAt.UnixMilli(),
	}
	for _, service := range value.Services {
		item := &uabv1.PreviewRuntimeService{
			Id: service.ID, Name: service.Name, WorkingDir: service.WorkingDir, Runtime: service.Runtime,
			RuntimeVersion: service.RuntimeVersion, Framework: service.Framework, PackageManager: service.PackageManager,
			Run: previewCommandProto(service.Run), Preview: service.Preview, ExpectedPorts: append([]uint32(nil), service.ExpectedPorts...), DependsOn: append([]string(nil), service.DependsOn...),
		}
		if service.Install != nil {
			item.Install = previewCommandProto(*service.Install)
		}
		result.Services = append(result.Services, item)
	}
	for _, evidence := range value.Evidence {
		result.Evidence = append(result.Evidence, &uabv1.PreviewDetectionEvidence{Detector: evidence.Detector, Path: evidence.Path, Reason: evidence.Reason, Score: evidence.Score})
	}
	return result
}

func previewCommandProto(value domain.RuntimeCommand) *uabv1.PreviewRuntimeCommand {
	environment := make(map[string]string, len(value.Env))
	for key, item := range value.Env {
		environment[key] = item
	}
	return &uabv1.PreviewRuntimeCommand{Command: value.Command, Args: append([]string(nil), value.Args...), Env: environment}
}

func previewInstanceProto(value domain.PreviewInstance) *uabv1.PreviewInstance {
	result := &uabv1.PreviewInstance{
		Id: value.ID, WorkspaceId: value.WorkspaceID, ProcessId: value.ProcessID, ServiceId: value.ServiceID,
		Framework: value.Framework, PlanFingerprint: value.PlanFingerprint, Port: value.Port, Status: value.Status,
		PublicUrl: value.PublicURL, StartedUnixMillis: value.StartedAt.UnixMilli(), UpdatedUnixMillis: value.UpdatedAt.UnixMilli(), LastError: value.LastError,
	}
	for _, process := range value.Processes {
		result.Processes = append(result.Processes, &uabv1.PreviewProcess{ServiceId: process.ServiceID, ProcessId: process.ProcessID, Port: process.Port, Status: process.Status})
	}
	return result
}

func previewChunkProto(value domain.ProcessChunk) *uabv1.PreviewLogChunk {
	return &uabv1.PreviewLogChunk{ProcessId: value.ProcessID, Cursor: value.Cursor, Data: append([]byte(nil), value.Data...), Exited: value.Exited, ExitCode: value.ExitCode}
}
