package connectapi

import (
	"context"

	"connectrpc.com/connect"
	uabv1 "github.com/omai/backend/gen/go/uab/v1"
	"github.com/omai/backend/internal/domain"
)

func (s *Services) Init(ctx context.Context, request *connect.Request[uabv1.GitInitRequest]) (*connect.Response[uabv1.GitInitResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	status, err := s.Git.Init(ctx, principal, request.Msg.GetWorkspaceId())
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.GitInitResponse{Status: gitStatusProto(status)}), nil
}

func (s *Services) Status(ctx context.Context, request *connect.Request[uabv1.GitStatusRequest]) (*connect.Response[uabv1.GitStatusResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	status, err := s.Git.Status(ctx, principal, request.Msg.GetWorkspaceId())
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.GitStatusResponse{Status: gitStatusProto(status)}), nil
}
func (s *Services) Diff(ctx context.Context, request *connect.Request[uabv1.GitDiffRequest]) (*connect.Response[uabv1.GitDiffResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	value, err := s.Git.Diff(ctx, principal, request.Msg.GetWorkspaceId(), request.Msg.GetPath(), request.Msg.GetStaged())
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.GitDiffResponse{Diff: value}), nil
}
func (s *Services) DiffFiles(ctx context.Context, request *connect.Request[uabv1.GitDiffFilesRequest]) (*connect.Response[uabv1.GitDiffFilesResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	values, err := s.Git.DiffFiles(
		ctx,
		principal,
		request.Msg.GetWorkspaceId(),
		request.Msg.GetMode(),
		request.Msg.GetPath(),
		int(request.Msg.GetContextLines()),
	)
	if err != nil {
		return nil, connectError(err)
	}
	response := &uabv1.GitDiffFilesResponse{}
	for _, value := range values {
		response.Files = append(response.Files, &uabv1.GitFileDiff{
			File: value.File, Patch: value.Patch, Additions: value.Additions,
			Deletions: value.Deletions, Status: value.Status,
		})
	}
	return connect.NewResponse(response), nil
}
func (s *Services) Stage(ctx context.Context, request *connect.Request[uabv1.GitStageRequest]) (*connect.Response[uabv1.GitStageResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	status, err := s.Git.Stage(ctx, principal, request.Msg.GetWorkspaceId(), request.Msg.GetPaths(), request.Msg.GetAll())
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.GitStageResponse{Status: gitStatusProto(status)}), nil
}
func (s *Services) Unstage(ctx context.Context, request *connect.Request[uabv1.GitUnstageRequest]) (*connect.Response[uabv1.GitUnstageResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	status, err := s.Git.Unstage(ctx, principal, request.Msg.GetWorkspaceId(), request.Msg.GetPaths(), request.Msg.GetAll())
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.GitUnstageResponse{Status: gitStatusProto(status)}), nil
}
func (s *Services) Commit(ctx context.Context, request *connect.Request[uabv1.GitCommitRequest]) (*connect.Response[uabv1.GitCommitResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	value, err := s.Git.Commit(ctx, principal, request.Msg.GetWorkspaceId(), request.Msg.GetMessage())
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.GitCommitResponse{Commit: value}), nil
}
func (s *Services) ListWorktrees(ctx context.Context, request *connect.Request[uabv1.ListWorktreesRequest]) (*connect.Response[uabv1.ListWorktreesResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	values, err := s.Git.ListWorktrees(ctx, principal, request.Msg.GetWorkspaceId())
	if err != nil {
		return nil, connectError(err)
	}
	response := &uabv1.ListWorktreesResponse{}
	for _, value := range values {
		response.Worktrees = append(response.Worktrees, &uabv1.WorktreeInfo{Path: value.Path, Branch: value.Branch, Head: value.Head})
	}
	return connect.NewResponse(response), nil
}
func (s *Services) CreateWorktree(ctx context.Context, request *connect.Request[uabv1.CreateWorktreeRequest]) (*connect.Response[uabv1.CreateWorktreeResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	value, err := s.Git.CreateWorktree(ctx, principal, request.Msg.GetWorkspaceId(), request.Msg.GetBranch(), request.Msg.GetBase())
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.CreateWorktreeResponse{Worktree: &uabv1.WorktreeInfo{Path: value.Path, Branch: value.Branch, Head: value.Head}}), nil
}
func (s *Services) RemoveWorktree(ctx context.Context, request *connect.Request[uabv1.RemoveWorktreeRequest]) (*connect.Response[uabv1.RemoveWorktreeResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Git.RemoveWorktree(ctx, principal, request.Msg.GetWorkspaceId(), request.Msg.GetPath()); err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.RemoveWorktreeResponse{}), nil
}
func (s *Services) Merge(ctx context.Context, request *connect.Request[uabv1.MergeRequest]) (*connect.Response[uabv1.MergeResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	commit, fastForward, err := s.Git.Merge(ctx, principal, request.Msg.GetWorkspaceId(), request.Msg.GetBranch())
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.MergeResponse{Commit: commit, FastForward: fastForward}), nil
}
func gitStatusProto(status domain.GitStatus) *uabv1.GitStatus {
	value := &uabv1.GitStatus{Branch: status.Branch, DefaultBranch: status.DefaultBranch}
	for _, file := range status.Files {
		value.Files = append(value.Files, &uabv1.GitFileStatus{Path: file.Path, Status: file.Status})
	}
	return value
}
