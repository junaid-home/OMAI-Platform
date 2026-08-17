package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/omai/backend/internal/voice/control"
	"github.com/omai/backend/internal/voice/protocol"
	"github.com/omai/backend/internal/voice/provider"
)

type frame struct {
	kind       websocket.MessageType
	data       []byte
	audioEpoch uint64
}
type Session struct {
	id, lease                    string
	browser                      *websocket.Conn
	provider                     provider.Session
	control                      *control.Client
	idle, heartbeat, toolTimeout time.Duration
	outbound                     chan frame
	calls                        chan protocol.ToolCall
	confirmations                chan protocol.ClientMessage
	uiResults                    chan protocol.ClientMessage
	toolPending                  atomic.Bool
	sequence                     atomic.Uint64
	audioEpoch                   atomic.Uint64
	once                         sync.Once
	toolCancelMu                 sync.Mutex
	activeToolID                 string
	activeToolCancel             context.CancelFunc
	cancelledCalls               map[string]struct{}
}

func New(id, lease string, browser *websocket.Conn, providerSession provider.Session, controlClient *control.Client, idle, heartbeat, toolTimeout time.Duration) *Session {
	return &Session{id: id, lease: lease, browser: browser, provider: providerSession, control: controlClient, idle: idle, heartbeat: heartbeat, toolTimeout: toolTimeout, outbound: make(chan frame, 128), calls: make(chan protocol.ToolCall, 32), confirmations: make(chan protocol.ClientMessage, 8), uiResults: make(chan protocol.ClientMessage, 16), cancelledCalls: make(map[string]struct{})}
}
func (s *Session) Run(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer s.close(context.Background())
	errorsChannel := make(chan error, 5)
	go func() { errorsChannel <- s.writeLoop(ctx) }()
	go func() { errorsChannel <- s.browserLoop(ctx) }()
	go func() { errorsChannel <- s.providerLoop(ctx) }()
	go func() { errorsChannel <- s.toolLoop(ctx) }()
	go func() { errorsChannel <- s.heartbeatLoop(ctx) }()
	err := <-errorsChannel
	cancel()
	return err
}
func (s *Session) close(ctx context.Context) {
	s.once.Do(func() {
		_ = s.provider.Close()
		_ = s.browser.Close(websocket.StatusNormalClosure, "session ended")
		s.control.Release(ctx, s.lease)
	})
}
func (s *Session) sendJSON(value protocol.ServerMessage) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.enqueue(frame{kind: websocket.MessageText, data: data})
}
func (s *Session) sendAudio(data []byte) error {
	return s.enqueue(frame{kind: websocket.MessageBinary, data: append([]byte(nil), data...), audioEpoch: s.audioEpoch.Load()})
}
func (s *Session) enqueue(value frame) error {
	select {
	case s.outbound <- value:
		return nil
	default:
		return errors.New("voice client backpressure limit reached")
	}
}
func (s *Session) writeLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case value := <-s.outbound:
			if s.staleAudio(value) {
				continue
			}
			writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := s.browser.Write(writeCtx, value.kind, value.data)
			cancel()
			if err != nil {
				return err
			}
		}
	}
}
func (s *Session) browserLoop(ctx context.Context) error {
	for {
		readCtx, cancel := context.WithTimeout(ctx, s.idle)
		kind, data, err := s.browser.Read(readCtx)
		cancel()
		if err != nil {
			return err
		}
		switch kind {
		case websocket.MessageBinary:
			if s.toolPending.Load() {
				continue
			}
			if len(data) == 0 || len(data) > 64<<10 {
				return errors.New("invalid PCM audio frame")
			}
			if err := s.provider.SendAudio(ctx, data); err != nil {
				return err
			}
		case websocket.MessageText:
			var message protocol.ClientMessage
			if json.Unmarshal(data, &message) != nil {
				return errors.New("invalid voice control message")
			}
			switch message.Type {
			case "interrupt":
				s.audioEpoch.Add(1)
				if err := s.provider.Interrupt(ctx); err != nil {
					return err
				}
			case "confirm":
				if !validRequestID(message.RequestID) {
					return errors.New("invalid confirmation request id")
				}
				select {
				case s.confirmations <- message:
				default:
					return errors.New("confirmation queue is full")
				}
			case "ui_result", "ui_command_result":
				if !validRequestID(message.RequestID) {
					return errors.New("invalid UI result request id")
				}
				select {
				case s.uiResults <- message:
				default:
					return errors.New("UI result queue is full")
				}
			case "ping":
				if err := s.sendJSON(protocol.ServerMessage{Type: "pong"}); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unsupported voice message %q", message.Type)
			}
		default:
			return errors.New("unsupported websocket frame")
		}
	}
}
func (s *Session) providerLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-s.provider.Events():
			if !ok {
				return errors.New("voice provider closed")
			}
			if event.Err != nil {
				return event.Err
			}
			for _, id := range event.CancelledCallIDs {
				s.cancelTool(id)
			}
			if event.Interrupted {
				s.audioEpoch.Add(1)
				if err := s.sendJSON(protocol.ServerMessage{Type: "interrupted"}); err != nil {
					return err
				}
			}
			for _, audio := range event.Audio {
				if err := s.sendAudio(audio); err != nil {
					return err
				}
			}
			for _, transcript := range event.Transcripts {
				if err := s.sendJSON(protocol.ServerMessage{Type: "transcript", Role: transcript.Role, Transcript: transcript.Text}); err != nil {
					return err
				}
			}
			if event.TurnComplete {
				if err := s.sendJSON(protocol.ServerMessage{Type: "turn_complete"}); err != nil {
					return err
				}
			}
			for _, call := range event.Calls {
				if call.ID == "" {
					call.ID = fmt.Sprintf("%s-%d", s.id, s.sequence.Add(1))
				}
				select {
				case s.calls <- call:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	}
}
func (s *Session) staleAudio(value frame) bool {
	return value.kind == websocket.MessageBinary && value.audioEpoch != s.audioEpoch.Load()
}
func (s *Session) toolLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case call := <-s.calls:
			s.handleTool(ctx, call)
		}
	}
}
func (s *Session) handleTool(ctx context.Context, call protocol.ToolCall) {
	if !s.toolPending.CompareAndSwap(false, true) {
		s.result(ctx, call, false, "TOOL_BUSY", nil)
		return
	}
	defer s.toolPending.Store(false)
	dispatchCtx, cancel := context.WithTimeout(ctx, s.toolTimeout)
	if !s.registerTool(call.ID, cancel) {
		cancel()
		return
	}
	defer func() { s.clearTool(call.ID); cancel() }()
	response, err := s.control.Dispatch(dispatchCtx, s.lease, call.ID, call.Name, call.Args, false)
	if err != nil {
		if errors.Is(dispatchCtx.Err(), context.Canceled) {
			return
		}
		s.result(ctx, call, false, "DISPATCH_FAILED", nil)
		return
	}
	if response.GetConfirmationRequired() {
		_ = s.sendJSON(protocol.ServerMessage{Type: "confirmation_required", RequestID: call.ID, Tool: call.Name, Message: response.GetMessage()})
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		for {
			select {
			case confirmation := <-s.confirmations:
				if confirmation.RequestID != call.ID {
					continue
				}
				if !confirmation.Confirmed {
					s.result(ctx, call, false, "CONFIRMATION_DENIED", nil)
					return
				}
				response, err = s.control.Dispatch(dispatchCtx, s.lease, call.ID, call.Name, call.Args, true)
				if err != nil {
					if errors.Is(dispatchCtx.Err(), context.Canceled) {
						return
					}
					s.result(ctx, call, false, "DISPATCH_FAILED", nil)
					return
				}
				goto executed
			case <-timer.C:
				s.result(ctx, call, false, "CONFIRMATION_TIMEOUT", nil)
				return
			case <-dispatchCtx.Done():
				return
			}
		}
	}
executed:
	if !response.GetSuccess() {
		s.result(ctx, call, false, response.GetCode(), nil)
		return
	}
	if response.GetResult() == nil {
		s.result(ctx, call, false, "EMPTY_TOOL_RESULT", nil)
		return
	}
	result := response.GetResult().AsMap()
	if command, ok := portalCommand(result); ok {
		result, err = s.executePortal(dispatchCtx, call, command)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			if errors.Is(err, context.DeadlineExceeded) {
				s.result(ctx, call, false, "TOOL_TIMEOUT", nil)
				return
			}
			s.result(ctx, call, false, uiErrorCode(err.Error()), nil)
			return
		}
	}
	payload, _ := json.Marshal(result)
	_ = s.sendJSON(protocol.ServerMessage{Type: "tool_result", RequestID: call.ID, Tool: call.Name, Payload: payload})
	s.result(ctx, call, true, "", result)
}
func (s *Session) registerTool(id string, cancel context.CancelFunc) bool {
	s.toolCancelMu.Lock()
	defer s.toolCancelMu.Unlock()
	if _, cancelled := s.cancelledCalls[id]; cancelled {
		delete(s.cancelledCalls, id)
		return false
	}
	s.activeToolID = id
	s.activeToolCancel = cancel
	return true
}
func (s *Session) clearTool(id string) {
	s.toolCancelMu.Lock()
	defer s.toolCancelMu.Unlock()
	if s.activeToolID == id {
		s.activeToolID = ""
		s.activeToolCancel = nil
	}
	delete(s.cancelledCalls, id)
}
func (s *Session) cancelTool(id string) {
	s.toolCancelMu.Lock()
	defer s.toolCancelMu.Unlock()
	if s.activeToolID == id && s.activeToolCancel != nil {
		s.activeToolCancel()
		return
	}
	if len(s.cancelledCalls) < 128 {
		s.cancelledCalls[id] = struct{}{}
	}
}

type clientCommand struct {
	action  string
	payload map[string]any
	timeout time.Duration
}

func portalCommand(result map[string]any) (clientCommand, bool) {
	raw, ok := result["command"].(map[string]any)
	if !ok {
		return clientCommand{}, false
	}
	action, _ := raw["action"].(string)
	payload, _ := raw["payload"].(map[string]any)
	milliseconds, _ := raw["timeoutMs"].(float64)
	if action == "" {
		return clientCommand{}, false
	}
	if math.IsNaN(milliseconds) || math.IsInf(milliseconds, 0) || milliseconds <= 0 || milliseconds > 30_000 {
		milliseconds = 5_000
	}
	timeout := time.Duration(milliseconds) * time.Millisecond
	return clientCommand{action: action, payload: payload, timeout: timeout}, true
}
func (s *Session) executePortal(ctx context.Context, call protocol.ToolCall, command clientCommand) (map[string]any, error) {
	payload, _ := json.Marshal(command.payload)
	timeoutMilliseconds := command.timeout.Milliseconds()
	if timeoutMilliseconds <= 0 || timeoutMilliseconds > 30_000 {
		timeoutMilliseconds = 5_000
	}
	if err := s.sendJSON(protocol.ServerMessage{Type: "ui_command", RequestID: call.ID, Tool: call.Name, Action: command.action, TimeoutMS: uint32(timeoutMilliseconds), Payload: payload}); err != nil {
		return nil, errors.New("UI_QUEUE_FAILED")
	}
	timer := time.NewTimer(command.timeout)
	defer timer.Stop()
	for {
		select {
		case result := <-s.uiResults:
			if result.RequestID != call.ID {
				continue
			}
			if !result.Success {
				return nil, errors.New(uiErrorCode(result.Code))
			}
			if len(result.Payload) == 0 {
				return map[string]any{"success": true}, nil
			}
			var value map[string]any
			if json.Unmarshal(result.Payload, &value) != nil {
				return nil, errors.New("UI_RESULT_INVALID")
			}
			return value, nil
		case <-timer.C:
			return nil, errors.New("UI_COMMAND_TIMEOUT")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
func validRequestID(value string) bool {
	return value != "" && len(value) <= 256 && !strings.ContainsAny(value, "\r\n\x00")
}
func uiErrorCode(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" || len(value) > 64 {
		return "UI_COMMAND_FAILED"
	}
	for _, character := range value {
		if character != '_' && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return "UI_COMMAND_FAILED"
		}
	}
	return value
}
func (s *Session) result(ctx context.Context, call protocol.ToolCall, success bool, code string, payload map[string]any) {
	response := map[string]any{"success": success}
	if code != "" {
		response["error"] = code
	}
	if payload != nil {
		response["result"] = payload
	}
	_ = s.provider.SendToolResult(ctx, protocol.ToolResult{ID: call.ID, Name: call.Name, Response: response})
}
func (s *Session) heartbeatLoop(ctx context.Context) error {
	ticker := time.NewTicker(s.heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			heartbeatCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := s.control.Heartbeat(heartbeatCtx, s.lease)
			cancel()
			if err != nil {
				return fmt.Errorf("voice lease heartbeat: %w", err)
			}
		}
	}
}
