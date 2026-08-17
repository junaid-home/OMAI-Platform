package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	uabv1 "github.com/omai/backend/gen/go/uab/v1"
	"github.com/omai/backend/internal/domain"
	"github.com/omai/backend/internal/port"
	reflectionregistry "github.com/omai/backend/internal/reflection"
	"google.golang.org/protobuf/types/known/structpb"
)

type VoiceControl struct {
	leases              port.VoiceLeaseStore
	dispatches          port.VoiceDispatchStore
	workspaces          port.WorkspaceRepository
	tools               *reflectionregistry.Registry
	invoker             port.ToolInvoker
	ticketTTL, leaseTTL time.Duration
	maxSessions         int
}

func NewVoiceControl(leases port.VoiceLeaseStore, dispatches port.VoiceDispatchStore, workspaces port.WorkspaceRepository, tools *reflectionregistry.Registry, invoker port.ToolInvoker, ticketTTL, leaseTTL time.Duration, maxSessions int) *VoiceControl {
	return &VoiceControl{leases: leases, dispatches: dispatches, workspaces: workspaces, tools: tools, invoker: invoker, ticketTTL: ticketTTL, leaseTTL: leaseTTL, maxSessions: maxSessions}
}
func (v *VoiceControl) Mint(ctx context.Context, principal domain.Principal, workspaceID, locale, voice string) (string, time.Time, error) {
	if workspaceID == "" {
		return "", time.Time{}, domain.ErrInvalid
	}
	workspace, err := v.workspaces.Get(ctx, principal, workspaceID)
	if err != nil {
		return "", time.Time{}, err
	}
	token, err := secureToken("vtk_")
	if err != nil {
		return "", time.Time{}, err
	}
	expires := time.Now().UTC().Add(v.ticketTTL)
	admission := domain.VoiceAdmission{TenantID: principal.TenantID, ActorID: principal.ActorID, SubjectKey: tokenDigest(principal.TenantID + "\x00" + principal.ActorID), WorkspaceID: workspaceID, WorkspaceRoot: workspace.Root, Permissions: append([]string(nil), principal.Permissions...), Locale: bounded(locale, "de-CH", 32), Voice: bounded(voice, "Kore", 64), ExpiresAt: expires}
	if err := v.leases.Issue(ctx, tokenDigest(token), admission); err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}
func (v *VoiceControl) Redeem(ctx context.Context, token, sessionID string) (domain.VoiceLease, error) {
	if token == "" || sessionID == "" || len(sessionID) > 256 {
		return domain.VoiceLease{}, domain.ErrInvalid
	}
	return v.leases.Redeem(ctx, tokenDigest(token), sessionID, v.maxSessions, v.leaseTTL)
}
func (v *VoiceControl) Heartbeat(ctx context.Context, token string) (domain.VoiceLease, error) {
	return v.leases.Heartbeat(ctx, token, v.leaseTTL)
}
func (v *VoiceControl) Release(ctx context.Context, token string) error {
	return v.leases.Release(ctx, token)
}
func (v *VoiceControl) Tools(ctx context.Context, token string) ([]*uabv1.ReflectedTool, string, error) {
	lease, err := v.leases.Get(ctx, token)
	if err != nil {
		return nil, "", err
	}
	tools, etag := v.tools.VoiceTools(lease.Admission.Permissions)
	for _, tool := range tools {
		if err := hideVoiceBoundFields(tool); err != nil {
			return nil, "", err
		}
	}
	return tools, etag + ":session-bound-v1", nil
}
func (v *VoiceControl) Dispatch(ctx context.Context, token, requestID, idempotencyKey, name, version string, arguments *structpb.Struct, confirmed bool) ([]byte, bool, bool, error) {
	lease, err := v.leases.Get(ctx, token)
	if err != nil {
		return nil, false, false, err
	}
	if requestID == "" || len(requestID) > 256 || strings.ContainsAny(requestID, "\r\n\x00") {
		return nil, false, false, domain.ErrInvalid
	}
	tool, ok := v.tools.Resolve(name, version)
	if !ok {
		return nil, false, false, domain.ErrNotFound
	}
	allowed, _, _ := v.Tools(ctx, token)
	eligible := false
	for _, candidate := range allowed {
		if candidate.GetName() == name {
			eligible = true
			break
		}
	}
	if !eligible {
		return nil, false, false, domain.ErrForbidden
	}
	if arguments == nil {
		arguments = &structpb.Struct{Fields: map[string]*structpb.Value{}}
	}
	argumentMap := arguments.AsMap()
	delete(argumentMap, "workspace_id")
	delete(argumentMap, "workspaceId")
	delete(argumentMap, "root")
	if field := tool.Input.Fields().ByName("workspace_id"); field != nil {
		argumentMap[field.JSONName()] = lease.Admission.WorkspaceID
	}
	if field := tool.Input.Fields().ByName("root"); field != nil {
		argumentMap[field.JSONName()] = lease.Admission.WorkspaceRoot
	}
	raw, err := json.Marshal(argumentMap)
	if err != nil {
		return nil, false, false, domain.ErrInvalid
	}
	if err := reflectionregistry.ValidateArguments(tool, raw); err != nil {
		return nil, false, false, fmt.Errorf("%w: %v", domain.ErrInvalid, err)
	}
	if requiresConfirmation(tool.Descriptor) && !confirmed {
		slog.InfoContext(ctx, "voice tool awaiting confirmation", "request_id", requestID, "tool", name, "tenant_id", lease.Admission.TenantID, "actor_id", lease.Admission.ActorID)
		return nil, true, false, nil
	}
	if idempotencyKey == "" {
		idempotencyKey = requestID
	}
	if len(idempotencyKey) > 256 || strings.ContainsAny(idempotencyKey, "\r\n\x00") {
		return nil, false, false, domain.ErrInvalid
	}
	timeout := time.Duration(tool.Descriptor.GetTimeoutMs()) * time.Millisecond
	if timeout <= 0 || timeout > 60*time.Second {
		timeout = 60 * time.Second
	}
	dispatchTTL := v.leaseTTL
	if minimum := timeout + 15*time.Second; dispatchTTL < minimum {
		dispatchTTL = minimum
	}
	key := tokenDigest(lease.Token + "\x00" + idempotencyKey)
	fingerprint := tokenDigest(name + "\x00" + tool.Descriptor.GetVersion() + "\x00" + string(raw))
	record, state, err := v.dispatches.Begin(ctx, key, fingerprint, dispatchTTL)
	if err != nil {
		return nil, false, false, err
	}
	if state == port.VoiceDispatchRunning {
		slog.WarnContext(ctx, "voice tool duplicate still running", "request_id", requestID, "tool", name, "tenant_id", lease.Admission.TenantID, "actor_id", lease.Admission.ActorID)
		return nil, false, false, domain.ErrConflict
	}
	if state == port.VoiceDispatchCached {
		slog.InfoContext(ctx, "voice tool replayed cached result", "request_id", requestID, "tool", name, "tenant_id", lease.Admission.TenantID, "actor_id", lease.Admission.ActorID)
		return record.Result, false, true, nil
	}
	principal := domain.Principal{TenantID: lease.Admission.TenantID, ActorID: lease.Admission.ActorID, Permissions: append([]string(nil), lease.Admission.Permissions...)}
	invokeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := v.invoker.Invoke(invokeCtx, principal, tool.Descriptor.GetGrpcMethod(), raw)
	if err != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cleanupCancel()
		_ = v.dispatches.Abort(cleanupCtx, key, fingerprint)
		slog.WarnContext(ctx, "voice tool execution failed", "request_id", requestID, "tool", name, "tenant_id", lease.Admission.TenantID, "actor_id", lease.Admission.ActorID, "error", err)
		return nil, false, false, err
	}
	completeCtx, completeCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer completeCancel()
	if err := v.dispatches.Complete(completeCtx, key, fingerprint, result, dispatchTTL); err != nil {
		return nil, false, false, err
	}
	slog.InfoContext(ctx, "voice tool executed", "request_id", requestID, "tool", name, "tenant_id", lease.Admission.TenantID, "actor_id", lease.Admission.ActorID)
	return result, false, false, nil
}
func requiresConfirmation(tool *uabv1.ReflectedTool) bool {
	if tool.GetConfirmation() == uabv1.ConfirmationPolicy_CONFIRMATION_POLICY_ALWAYS {
		return true
	}
	return tool.GetConfirmation() == uabv1.ConfirmationPolicy_CONFIRMATION_POLICY_RISKY && (tool.GetRisk() == uabv1.RiskLevel_RISK_LEVEL_WRITE || tool.GetRisk() == uabv1.RiskLevel_RISK_LEVEL_DANGEROUS)
}
func secureToken(prefix string) (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(value[:]), nil
}
func tokenDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func bounded(value, fallback string, max int) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > max || strings.ContainsAny(value, "\r\n\x00") {
		return fallback
	}
	return value
}

func hideVoiceBoundFields(tool *uabv1.ReflectedTool) error {
	var schema map[string]any
	if err := json.Unmarshal(tool.GetInputSchemaJson(), &schema); err != nil {
		return fmt.Errorf("decode reflected voice schema: %w", err)
	}
	properties, _ := schema["properties"].(map[string]any)
	delete(properties, "workspaceId")
	delete(properties, "workspace_id")
	delete(properties, "root")
	if required, ok := schema["required"].([]any); ok {
		filtered := make([]any, 0, len(required))
		for _, value := range required {
			name, _ := value.(string)
			if name != "workspaceId" && name != "workspace_id" && name != "root" {
				filtered = append(filtered, value)
			}
		}
		schema["required"] = filtered
	}
	fields := make([]string, 0, len(tool.GetRequiredFields()))
	for _, name := range tool.GetRequiredFields() {
		if name != "workspace_id" && name != "root" {
			fields = append(fields, name)
		}
	}
	tool.RequiredFields = fields
	encoded, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("encode reflected voice schema: %w", err)
	}
	tool.InputSchemaJson = encoded
	return nil
}
