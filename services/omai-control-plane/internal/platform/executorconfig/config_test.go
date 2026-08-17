package executorconfig

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestProductionRequiresPinnedWorkspaceAndMutualTLS(t *testing.T) {
	t.Setenv("OMAI_EXECUTOR_ENV", "production")
	t.Setenv("OMAI_EXECUTOR_ADDR", "127.0.0.1:8792")
	t.Setenv("OMAI_EXECUTOR_WORKSPACE_ROOT", t.TempDir())
	t.Setenv("OMAI_EXECUTOR_TOKEN", "executor-token-0123456789-abcdef")
	t.Setenv("OMAI_EXECUTOR_TENANT_ID", "")
	t.Setenv("OMAI_EXECUTOR_WORKSPACE_ID", "")
	t.Setenv("OMAI_EXECUTOR_TLS_CERT", "")
	t.Setenv("OMAI_EXECUTOR_TLS_KEY", "")
	t.Setenv("OMAI_EXECUTOR_CLIENT_CA", "")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "one tenant and workspace") || !strings.Contains(err.Error(), "mutual TLS") {
		t.Fatalf("production validation error = %v", err)
	}
}

func TestHarnessRequiresPinnedIdentityLoopbackEdgeAndModelGateway(t *testing.T) {
	workspace := t.TempDir()
	home := filepath.Join(t.TempDir(), "harness")
	t.Setenv("OMAI_EXECUTOR_ENV", "development")
	t.Setenv("OMAI_EXECUTOR_ADDR", "127.0.0.1:8792")
	t.Setenv("OMAI_EXECUTOR_WORKSPACE_ROOT", workspace)
	t.Setenv("OMAI_EXECUTOR_TOKEN", "executor-token-0123456789-abcdef")
	t.Setenv("OMAI_EXECUTOR_TENANT_ID", "tenant")
	t.Setenv("OMAI_EXECUTOR_WORKSPACE_ID", "workspace")
	t.Setenv("OMAI_HARNESS_DRIVER", "opencode")
	t.Setenv("OMAI_HARNESS_COMMAND", "opencode")
	t.Setenv("OMAI_HARNESS_COMMAND_ARGS", `["--conditions=browser","/opt/opencode/src/index.ts"]`)
	t.Setenv("OMAI_HARNESS_HOME", home)
	t.Setenv("OMAI_HARNESS_STATE_FILE", filepath.Join(home, "sessions.json"))
	t.Setenv("OMAI_HARNESS_MODEL_EDGE_ADDR", "127.0.0.1:8793")
	t.Setenv("OMAI_HARNESS_MODEL_GATEWAY_URL", "http://127.0.0.1:8790")
	t.Setenv("OMAI_HARNESS_MODEL_GATEWAY_TOKEN", strings.Repeat("g", 32))
	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.HarnessDriver != "opencode" || config.HarnessHome != home || len(config.HarnessCommandArgs) != 2 || config.HarnessCommandArgs[1] != "/opt/opencode/src/index.ts" {
		t.Fatalf("harness configuration was not loaded: %#v", config)
	}

	t.Setenv("OMAI_HARNESS_MODEL_EDGE_ADDR", "0.0.0.0:8793")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("public model edge was accepted: %v", err)
	}
}

func TestProductionHarnessRequiresGatewayMutualTLS(t *testing.T) {
	workspace := t.TempDir()
	home := filepath.Join(t.TempDir(), "harness")
	t.Setenv("OMAI_EXECUTOR_ENV", "production")
	t.Setenv("OMAI_EXECUTOR_ADDR", "127.0.0.1:8792")
	t.Setenv("OMAI_EXECUTOR_WORKSPACE_ROOT", workspace)
	t.Setenv("OMAI_EXECUTOR_TOKEN", "executor-token-0123456789-abcdef")
	t.Setenv("OMAI_EXECUTOR_TENANT_ID", "tenant")
	t.Setenv("OMAI_EXECUTOR_WORKSPACE_ID", "workspace")
	t.Setenv("OMAI_EXECUTOR_TLS_CERT", "/cert")
	t.Setenv("OMAI_EXECUTOR_TLS_KEY", "/key")
	t.Setenv("OMAI_EXECUTOR_CLIENT_CA", "/ca")
	t.Setenv("OMAI_HARNESS_DRIVER", "opencode")
	t.Setenv("OMAI_HARNESS_COMMAND", "opencode")
	t.Setenv("OMAI_HARNESS_HOME", home)
	t.Setenv("OMAI_HARNESS_MODEL_GATEWAY_URL", "http://127.0.0.1:8790")
	t.Setenv("OMAI_HARNESS_MODEL_GATEWAY_TOKEN", strings.Repeat("g", 32))
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "must use HTTPS") || !strings.Contains(err.Error(), "requires mutual TLS") {
		t.Fatalf("unsafe production model gateway was accepted: %v", err)
	}
}

func TestDevelopmentAllowsLoopbackExecutor(t *testing.T) {
	t.Setenv("OMAI_EXECUTOR_ENV", "development")
	t.Setenv("OMAI_EXECUTOR_ADDR", "127.0.0.1:8792")
	t.Setenv("OMAI_EXECUTOR_WORKSPACE_ROOT", t.TempDir())
	t.Setenv("OMAI_EXECUTOR_TOKEN", "executor-token-0123456789-abcdef")
	t.Setenv("OMAI_EXECUTOR_TENANT_ID", "")
	t.Setenv("OMAI_EXECUTOR_WORKSPACE_ID", "")
	t.Setenv("OMAI_EXECUTOR_TLS_CERT", "")
	t.Setenv("OMAI_EXECUTOR_TLS_KEY", "")
	t.Setenv("OMAI_EXECUTOR_CLIENT_CA", "")
	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.WorkspaceRoot == "" || config.Token == "" {
		t.Fatalf("invalid development config: %#v", config)
	}
}
