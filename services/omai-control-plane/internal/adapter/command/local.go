package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/omai/backend/internal/domain"
	"github.com/omai/backend/internal/port"
)

type Local struct {
	workspaces port.WorkspaceRepository
	maxOutput  int
	maxRuntime time.Duration
}

func NewLocal(workspaces port.WorkspaceRepository, maxOutput int, maxRuntime time.Duration) *Local {
	return &Local{workspaces: workspaces, maxOutput: maxOutput, maxRuntime: maxRuntime}
}

func (r *Local) Run(ctx context.Context, principal domain.Principal, spec domain.CommandSpec) (domain.CommandResult, error) {
	if spec.WorkspaceID == "" || !validCommand(spec.Command) || len(spec.Args) > 256 || len(spec.Env) > 64 || spec.MaxOutputBytes <= 0 || spec.MaxOutputBytes > r.maxOutput {
		return domain.CommandResult{}, domain.ErrInvalid
	}
	for _, argument := range spec.Args {
		if len(argument) > 64<<10 || strings.ContainsRune(argument, '\x00') {
			return domain.CommandResult{}, domain.ErrInvalid
		}
	}
	workspace, err := r.workspaces.Get(ctx, principal, spec.WorkspaceID)
	if err != nil {
		return domain.CommandResult{}, err
	}
	cwd, err := commandCWD(workspace.Root, spec.CWD)
	if err != nil {
		return domain.CommandResult{}, err
	}
	environment, err := commandEnvironment(spec.Env, workspace.Root)
	if err != nil {
		return domain.CommandResult{}, err
	}
	commandContext, cancel := context.WithTimeout(ctx, r.maxRuntime)
	defer cancel()
	// #nosec G204 -- arbitrary argv execution is this adapter's explicit contract;
	// inputs, environment, working directory, time and output are bounded above.
	command := exec.CommandContext(commandContext, spec.Command, spec.Args...)
	command.Dir = cwd
	command.Env = environment
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = 2 * time.Second
	output := &boundedBuffer{limit: spec.MaxOutputBytes}
	command.Stdout = output
	command.Stderr = output
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	if err := command.Start(); err != nil {
		return domain.CommandResult{}, fmt.Errorf("start workspace command: %w", err)
	}
	waitErr := command.Wait()
	result := domain.CommandResult{Output: append([]byte(nil), output.Bytes()...), WorkspaceRoot: workspace.Root}
	if output.truncated {
		return domain.CommandResult{}, domain.ErrOutputTruncated
	}
	if waitErr == nil {
		return result, nil
	}
	if err := commandContext.Err(); err != nil {
		return domain.CommandResult{}, err
	}
	var exitError *exec.ExitError
	if !errors.As(waitErr, &exitError) {
		return domain.CommandResult{}, fmt.Errorf("wait for workspace command: %w", waitErr)
	}
	exitCode := exitError.ExitCode()
	if exitCode < -1<<31 || exitCode > 1<<31-1 {
		result.ExitCode = -1
	} else {
		result.ExitCode = int32(exitCode)
	}
	return result, nil
}

type boundedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedBuffer) Bytes() []byte { return b.buffer.Bytes() }

func (b *boundedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = b.truncated || original > 0
		return original, nil
	}
	if len(data) > remaining {
		data = data[:remaining]
		b.truncated = true
	}
	_, _ = b.buffer.Write(data)
	return original, nil
}

func commandCWD(root, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return root, nil
	}
	if filepath.IsAbs(requested) || strings.ContainsRune(requested, '\x00') {
		return "", domain.ErrInvalid
	}
	candidate := filepath.Join(root, filepath.Clean(requested))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", domain.ErrInvalid
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", domain.ErrForbidden
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", domain.ErrInvalid
	}
	return resolved, nil
}

func commandEnvironment(values map[string]string, workspaceRoot string) ([]string, error) {
	base := map[string]string{
		"PATH": os.Getenv("PATH"), "HOME": os.TempDir(), "LANG": os.Getenv("LANG"),
		"LC_ALL": os.Getenv("LC_ALL"), "TERM": "dumb", "OMAI_WORKSPACE_ROOT": workspaceRoot,
	}
	for name, value := range values {
		if !validEnvironmentName(name) || len(value) > 64<<10 || strings.ContainsRune(value, '\x00') || strings.HasPrefix(name, "OMAI_") {
			return nil, domain.ErrInvalid
		}
		base[name] = value
	}
	result := make([]string, 0, len(base))
	for name, value := range base {
		if value != "" {
			result = append(result, name+"="+value)
		}
	}
	return result, nil
}

func validEnvironmentName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, character := range value {
		if !(character == '_' || character >= 'A' && character <= 'Z' || index > 0 && character >= '0' && character <= '9') {
			return false
		}
	}
	return true
}

func validCommand(value string) bool {
	return value != "" && len(value) <= 4096 && !strings.ContainsAny(value, "\r\n\x00")
}
