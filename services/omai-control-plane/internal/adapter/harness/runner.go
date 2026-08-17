package harness

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/omai/backend/internal/domain"
)

type LocalRunner struct {
	maxLineBytes   int
	maxStderrBytes int
	waitDelay      time.Duration
}

func NewLocalRunner(maxLineBytes, maxStderrBytes int) *LocalRunner {
	return &LocalRunner{maxLineBytes: maxLineBytes, maxStderrBytes: maxStderrBytes, waitDelay: 2 * time.Second}
}

func (*LocalRunner) Available(command string) error {
	if _, err := exec.LookPath(command); err != nil {
		return fmt.Errorf("harness executable %q is unavailable: %w", command, err)
	}
	return nil
}

func (r *LocalRunner) Run(ctx context.Context, invocation Invocation, consume func([]byte) error) (RunResult, error) {
	if invocation.Command == "" || len(invocation.Args) > 256 || len(invocation.Env) > 128 {
		return RunResult{}, domainInvalid("invalid harness invocation")
	}
	// #nosec G204 -- the command is operator configured, argv is constructed by a
	// reviewed driver, and this runner executes only inside the workspace sandbox.
	command := exec.CommandContext(ctx, invocation.Command, invocation.Args...)
	command.Dir = invocation.Dir
	command.Env = append([]string(nil), invocation.Env...)
	command.Stdin = bytes.NewReader(invocation.Stdin)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = r.waitDelay
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return RunResult{}, fmt.Errorf("open harness stdout: %w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return RunResult{}, fmt.Errorf("open harness stderr: %w", err)
	}
	if err := command.Start(); err != nil {
		return RunResult{}, fmt.Errorf("start harness: %w", err)
	}

	var stderrBuffer boundedText
	stderrBuffer.limit = r.maxStderrBytes
	var stderrErr error
	var stderrWait sync.WaitGroup
	stderrWait.Add(1)
	go func() {
		defer stderrWait.Done()
		_, stderrErr = io.Copy(&stderrBuffer, stderr)
	}()

	scanner := bufio.NewScanner(stdout)
	initialBuffer := 64 << 10
	if r.maxLineBytes < initialBuffer {
		initialBuffer = r.maxLineBytes
	}
	scanner.Buffer(make([]byte, initialBuffer), r.maxLineBytes)
	var consumeErr error
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if err := consume(line); err != nil {
			consumeErr = err
			_ = command.Cancel()
			break
		}
	}
	scanErr := scanner.Err()
	stderrWait.Wait()
	waitErr := command.Wait()
	result := RunResult{Stderr: strings.TrimSpace(stderrBuffer.String())}
	var exitError *exec.ExitError
	if errors.As(waitErr, &exitError) {
		result.ExitCode = exitError.ExitCode()
	}
	if consumeErr != nil {
		return result, consumeErr
	}
	if scanErr != nil {
		return result, fmt.Errorf("read harness event stream: %w", scanErr)
	}
	if stderrErr != nil {
		return result, fmt.Errorf("read harness stderr: %w", stderrErr)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if waitErr != nil && result.ExitCode == 0 {
		return result, fmt.Errorf("wait for harness: %w", waitErr)
	}
	return result, nil
}

type boundedText struct {
	buffer bytes.Buffer
	limit  int
}

func (b *boundedText) Write(data []byte) (int, error) {
	original := len(data)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = b.buffer.Write(data)
	}
	return original, nil
}

func (b *boundedText) String() string { return b.buffer.String() }

func domainInvalid(message string) error { return fmt.Errorf("%w: %s", domain.ErrInvalid, message) }
