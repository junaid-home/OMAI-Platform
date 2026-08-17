package harness

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestLocalRunnerStreamsLinesAndBoundsStderr(t *testing.T) {
	runner := NewLocalRunner(1024, 8)
	var lines []string
	result, err := runner.Run(context.Background(), Invocation{
		Command: "/bin/sh", Args: []string{"-c", "printf 'one\\ntwo\\n'; printf '0123456789' >&2"},
		Env: []string{"PATH=/usr/bin:/bin"}, Dir: t.TempDir(),
	}, func(line []byte) error {
		lines = append(lines, string(line))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(lines, ",") != "one,two" || result.Stderr != "01234567" || result.ExitCode != 0 {
		t.Fatalf("unexpected runner result: %#v, %#v", lines, result)
	}
}

func TestLocalRunnerCancelsProcessGroup(t *testing.T) {
	runner := NewLocalRunner(1024, 1024)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := runner.Run(ctx, Invocation{
		Command: "/bin/sh", Args: []string{"-c", "sleep 30 & wait"},
		Env: []string{"PATH=/usr/bin:/bin"}, Dir: t.TempDir(),
	}, func([]byte) error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unexpected cancellation error: %v", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("process group cancellation was not bounded")
	}
}

func TestLocalRunnerRejectsOversizedEvent(t *testing.T) {
	runner := NewLocalRunner(32, 1024)
	_, err := runner.Run(context.Background(), Invocation{
		Command: "/bin/sh", Args: []string{"-c", "printf '%0100d\\n' 0"},
		Env: []string{"PATH=/usr/bin:/bin"}, Dir: t.TempDir(),
	}, func([]byte) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "token too long") {
		t.Fatalf("oversized event was not rejected: %v", err)
	}
}
