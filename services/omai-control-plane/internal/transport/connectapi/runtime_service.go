package connectapi

import (
	"context"

	"connectrpc.com/connect"
	uabv1 "github.com/omai/backend/gen/go/uab/v1"
	"github.com/omai/backend/internal/domain"
)

type RuntimeService struct{ Core *Services }

func (s *RuntimeService) Health(ctx context.Context, request *connect.Request[uabv1.RuntimeHealthRequest]) (*connect.Response[uabv1.RuntimeHealthResponse], error) {
	runtimeAdapter, ok := s.Core.Runtimes.Get(request.Msg.GetRuntimeId())
	if !ok {
		return nil, connectError(domain.ErrNotFound)
	}
	return connect.NewResponse(runtimeHealthProto(runtimeAdapter.Health(ctx))), nil
}

func (s *RuntimeService) ListHealth(ctx context.Context, request *connect.Request[uabv1.ListRuntimeHealthRequest]) (*connect.Response[uabv1.ListRuntimeHealthResponse], error) {
	return s.Core.ListHealth(ctx, request)
}
