package config

import (
	"strings"
	"testing"
)

func TestLoopbackAddress(t *testing.T) {
	for _, value := range []string{"127.0.0.1:8787", "[::1]:8787", "localhost:8787"} {
		if !loopbackAddress(value) {
			t.Fatalf("expected loopback: %s", value)
		}
	}
	if loopbackAddress("0.0.0.0:8787") {
		t.Fatal("wildcard is not loopback")
	}
}

func TestProductionRequiresRemoteExecutor(t *testing.T) {
	t.Setenv("OMAI_ENV", "production")
	t.Setenv("OMAI_WORKSPACE_ROOTS", t.TempDir())
	t.Setenv("OMAI_ALLOWED_ORIGINS", "https://portal.example")
	t.Setenv("OMAI_SERVICE_TOKEN", "service-token-0123456789-abcdefgh")
	t.Setenv("OMAI_REDIS_ADDR", "127.0.0.1:6379")
	t.Setenv("OMAI_EXECUTOR_URL", "")
	t.Setenv("OMAI_DEV_TOKEN", "")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "remote workspace executor") {
		t.Fatalf("production validation error = %v", err)
	}
}
