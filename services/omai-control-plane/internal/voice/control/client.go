package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	uabv1 "github.com/omai/backend/gen/go/uab/v1"
	uabv1connect "github.com/omai/backend/gen/go/uab/v1/uabv1connect"
	"github.com/omai/backend/internal/voice/provider"
	"google.golang.org/protobuf/types/known/structpb"
)

type Client struct {
	api uabv1connect.VoiceControlServiceClient
}

func New(baseURL, token string) *Client {
	transport := &tokenTransport{base: http.DefaultTransport, token: token}
	httpClient := &http.Client{Transport: transport, Timeout: 70 * time.Second}
	return &Client{api: uabv1connect.NewVoiceControlServiceClient(httpClient, strings.TrimRight(baseURL, "/"))}
}
func (c *Client) Redeem(ctx context.Context, ticket, sessionID string) (*uabv1.RedeemVoiceTicketResponse, error) {
	response, err := c.api.RedeemTicket(ctx, connect.NewRequest(&uabv1.RedeemVoiceTicketRequest{Ticket: ticket, SessionId: sessionID}))
	if err != nil {
		return nil, err
	}
	return response.Msg, nil
}
func (c *Client) Tools(ctx context.Context, lease string) ([]provider.Tool, string, error) {
	response, err := c.api.ListVoiceTools(ctx, connect.NewRequest(&uabv1.ListVoiceToolsRequest{LeaseToken: lease}))
	if err != nil {
		return nil, "", err
	}
	result := make([]provider.Tool, 0, len(response.Msg.GetTools()))
	for _, tool := range response.Msg.GetTools() {
		var schema map[string]any
		if err := json.Unmarshal(tool.GetInputSchemaJson(), &schema); err != nil {
			return nil, "", fmt.Errorf("decode tool schema: %w", err)
		}
		result = append(result, provider.Tool{Name: tool.GetName(), Description: tool.GetDescription(), Parameters: schema})
	}
	return result, response.Msg.GetRegistryEtag(), nil
}
func (c *Client) Heartbeat(ctx context.Context, lease string) error {
	_, err := c.api.Heartbeat(ctx, connect.NewRequest(&uabv1.VoiceLeaseRequest{LeaseToken: lease}))
	return err
}
func (c *Client) Release(ctx context.Context, lease string) {
	releaseCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, _ = c.api.Release(releaseCtx, connect.NewRequest(&uabv1.VoiceLeaseRequest{LeaseToken: lease}))
}
func (c *Client) Dispatch(ctx context.Context, lease, requestID, tool string, args map[string]any, confirmed bool) (*uabv1.VoiceDispatchResponse, error) {
	arguments, err := structpb.NewStruct(args)
	if err != nil {
		return nil, err
	}
	response, err := c.api.Dispatch(ctx, connect.NewRequest(&uabv1.VoiceDispatchRequest{LeaseToken: lease, RequestId: requestID, Tool: tool, Version: "v1", Arguments: arguments, Confirmed: confirmed, IdempotencyKey: requestID}))
	if err != nil {
		return nil, err
	}
	return response.Msg, nil
}

type tokenTransport struct {
	base  http.RoundTripper
	token string
}

func (t *tokenTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+t.token)
	clone.Header.Set("X-OMAI-Tenant-ID", "system")
	clone.Header.Set("X-OMAI-Actor-ID", "voice-gateway")
	return t.base.RoundTrip(clone)
}
