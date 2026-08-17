package executorconfig

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Environment               string
	Addr                      string
	WorkspaceRoot             string
	Token                     string
	AllowedTenant             string
	ExpectedWorkspaceID       string
	TLSCert                   string
	TLSKey                    string
	ClientCA                  string
	AllowInsecure             bool
	EnableReflection          bool
	MaxBodyBytes              int64
	MaxFileBytes              int64
	MaxArchiveBytes           int64
	ProcessBuffer             int
	MaxProcesses              int
	MaxCommandOutput          int
	CommandTimeout            time.Duration
	GracePeriod               time.Duration
	HarnessDriver             string
	HarnessCommand            string
	HarnessCommandArgs        []string
	HarnessVersion            string
	HarnessHome               string
	HarnessStateFile          string
	HarnessModelEdgeAddr      string
	HarnessAutoApprove        bool
	HarnessMaxLineBytes       int
	HarnessMaxStderrBytes     int
	HarnessLeaseTTL           time.Duration
	ModelGatewayEndpoint      string
	ModelGatewayToken         string
	ModelGatewayTransport     string
	ModelGatewayCACert        string
	ModelGatewayClientCert    string
	ModelGatewayClientKey     string
	ModelGatewayTLSServerName string
}

func Load() (Config, error) {
	var errs []error
	config := Config{
		Environment:               environment("OMAI_EXECUTOR_ENV", "development"),
		Addr:                      environment("OMAI_EXECUTOR_ADDR", "127.0.0.1:8792"),
		Token:                     strings.TrimSpace(os.Getenv("OMAI_EXECUTOR_TOKEN")),
		AllowedTenant:             strings.TrimSpace(os.Getenv("OMAI_EXECUTOR_TENANT_ID")),
		ExpectedWorkspaceID:       strings.TrimSpace(os.Getenv("OMAI_EXECUTOR_WORKSPACE_ID")),
		TLSCert:                   strings.TrimSpace(os.Getenv("OMAI_EXECUTOR_TLS_CERT")),
		TLSKey:                    strings.TrimSpace(os.Getenv("OMAI_EXECUTOR_TLS_KEY")),
		ClientCA:                  strings.TrimSpace(os.Getenv("OMAI_EXECUTOR_CLIENT_CA")),
		AllowInsecure:             boolean("OMAI_EXECUTOR_ALLOW_INSECURE", false, &errs),
		EnableReflection:          boolean("OMAI_EXECUTOR_ENABLE_REFLECTION", false, &errs),
		GracePeriod:               duration("OMAI_EXECUTOR_GRACE_PERIOD", 15*time.Second, &errs),
		CommandTimeout:            duration("OMAI_EXECUTOR_COMMAND_TIMEOUT", 2*time.Minute, &errs),
		HarnessDriver:             strings.TrimSpace(os.Getenv("OMAI_HARNESS_DRIVER")),
		HarnessCommand:            strings.TrimSpace(os.Getenv("OMAI_HARNESS_COMMAND")),
		HarnessVersion:            environment("OMAI_HARNESS_VERSION", "unknown"),
		HarnessModelEdgeAddr:      environment("OMAI_HARNESS_MODEL_EDGE_ADDR", "127.0.0.1:8793"),
		HarnessAutoApprove:        boolean("OMAI_HARNESS_AUTO_APPROVE", false, &errs),
		HarnessLeaseTTL:           duration("OMAI_HARNESS_MODEL_LEASE_TTL", 2*time.Hour, &errs),
		ModelGatewayEndpoint:      strings.TrimSpace(os.Getenv("OMAI_HARNESS_MODEL_GATEWAY_URL")),
		ModelGatewayToken:         strings.TrimSpace(os.Getenv("OMAI_HARNESS_MODEL_GATEWAY_TOKEN")),
		ModelGatewayTransport:     environment("OMAI_HARNESS_MODEL_GATEWAY_TRANSPORT", "grpc"),
		ModelGatewayCACert:        strings.TrimSpace(os.Getenv("OMAI_HARNESS_MODEL_GATEWAY_CA_CERT")),
		ModelGatewayClientCert:    strings.TrimSpace(os.Getenv("OMAI_HARNESS_MODEL_GATEWAY_CLIENT_CERT")),
		ModelGatewayClientKey:     strings.TrimSpace(os.Getenv("OMAI_HARNESS_MODEL_GATEWAY_CLIENT_KEY")),
		ModelGatewayTLSServerName: strings.TrimSpace(os.Getenv("OMAI_HARNESS_MODEL_GATEWAY_TLS_SERVER_NAME")),
	}
	config.MaxBodyBytes = int64(positiveInteger("OMAI_EXECUTOR_MAX_BODY_BYTES", 1<<20, &errs))
	config.MaxFileBytes = int64(positiveInteger("OMAI_EXECUTOR_MAX_FILE_BYTES", 4<<20, &errs))
	config.MaxArchiveBytes = int64(positiveInteger("OMAI_EXECUTOR_MAX_ARCHIVE_BYTES", 200<<20, &errs))
	config.ProcessBuffer = positiveInteger("OMAI_EXECUTOR_PROCESS_BUFFER", 4<<20, &errs)
	config.MaxProcesses = positiveInteger("OMAI_EXECUTOR_MAX_PROCESSES", 32, &errs)
	config.MaxCommandOutput = positiveInteger("OMAI_EXECUTOR_MAX_COMMAND_OUTPUT", 16<<20, &errs)
	config.HarnessMaxLineBytes = positiveInteger("OMAI_HARNESS_MAX_EVENT_BYTES", 4<<20, &errs)
	config.HarnessMaxStderrBytes = positiveInteger("OMAI_HARNESS_MAX_STDERR_BYTES", 1<<20, &errs)
	config.HarnessCommandArgs = stringList("OMAI_HARNESS_COMMAND_ARGS", &errs)

	root := strings.TrimSpace(os.Getenv("OMAI_EXECUTOR_WORKSPACE_ROOT"))
	if root == "" {
		errs = append(errs, errors.New("OMAI_EXECUTOR_WORKSPACE_ROOT is required"))
	} else if absolute, err := filepath.Abs(root); err != nil {
		errs = append(errs, fmt.Errorf("executor workspace root: %w", err))
	} else if resolved, err := filepath.EvalSymlinks(absolute); err != nil {
		errs = append(errs, fmt.Errorf("executor workspace root: %w", err))
	} else if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
		errs = append(errs, errors.New("executor workspace root must be an existing directory"))
	} else {
		config.WorkspaceRoot = filepath.Clean(resolved)
	}
	if len(config.Token) < 32 {
		errs = append(errs, errors.New("OMAI_EXECUTOR_TOKEN must contain at least 32 characters"))
	}
	harnessHome := strings.TrimSpace(os.Getenv("OMAI_HARNESS_HOME"))
	if harnessHome == "" {
		harnessHome = filepath.Join(os.TempDir(), "omai-harness")
	}
	if !filepath.IsAbs(harnessHome) {
		errs = append(errs, errors.New("OMAI_HARNESS_HOME must be absolute"))
	} else {
		config.HarnessHome = filepath.Clean(harnessHome)
	}
	harnessState := strings.TrimSpace(os.Getenv("OMAI_HARNESS_STATE_FILE"))
	if harnessState == "" {
		harnessState = filepath.Join(config.HarnessHome, "sessions.json")
	}
	if !filepath.IsAbs(harnessState) {
		errs = append(errs, errors.New("OMAI_HARNESS_STATE_FILE must be absolute"))
	} else {
		config.HarnessStateFile = filepath.Clean(harnessState)
	}
	if config.HarnessDriver != "" {
		if config.HarnessDriver != "opencode" {
			errs = append(errs, errors.New("OMAI_HARNESS_DRIVER currently supports opencode"))
		}
		if config.HarnessCommand == "" || strings.ContainsRune(config.HarnessCommand, '\x00') {
			errs = append(errs, errors.New("OMAI_HARNESS_COMMAND is required when a harness driver is enabled"))
		}
		if config.AllowedTenant == "" || config.ExpectedWorkspaceID == "" {
			errs = append(errs, errors.New("a harness-enabled executor requires one tenant and workspace identity"))
		}
		if _, _, err := net.SplitHostPort(config.HarnessModelEdgeAddr); err != nil || !loopback(config.HarnessModelEdgeAddr) {
			errs = append(errs, errors.New("OMAI_HARNESS_MODEL_EDGE_ADDR must be a loopback host and port"))
		}
		gateway, err := url.Parse(config.ModelGatewayEndpoint)
		if err != nil || gateway.Host == "" || gateway.User != nil || gateway.RawQuery != "" || gateway.Fragment != "" || (gateway.Scheme != "http" && gateway.Scheme != "https") {
			errs = append(errs, errors.New("OMAI_HARNESS_MODEL_GATEWAY_URL must be an absolute HTTP(S) URL"))
		}
		if len(config.ModelGatewayToken) < 32 {
			errs = append(errs, errors.New("OMAI_HARNESS_MODEL_GATEWAY_TOKEN must contain at least 32 characters"))
		}
		if config.ModelGatewayTransport != "connect" && config.ModelGatewayTransport != "grpc" {
			errs = append(errs, errors.New("OMAI_HARNESS_MODEL_GATEWAY_TRANSPORT must be connect or grpc"))
		}
		if (config.ModelGatewayClientCert == "") != (config.ModelGatewayClientKey == "") {
			errs = append(errs, errors.New("model gateway client certificate and key must be set together"))
		}
		if contained(config.WorkspaceRoot, config.HarnessHome) || contained(config.WorkspaceRoot, config.HarnessStateFile) {
			errs = append(errs, errors.New("harness home and state must remain outside the agent workspace"))
		}
		if config.Environment == "production" {
			if !filepath.IsAbs(config.HarnessCommand) {
				errs = append(errs, errors.New("production harness command must be absolute"))
			}
			if gateway == nil || gateway.Scheme != "https" {
				errs = append(errs, errors.New("production harness model gateway must use HTTPS"))
			}
			if config.ModelGatewayCACert == "" || config.ModelGatewayClientCert == "" {
				errs = append(errs, errors.New("production harness model gateway requires mutual TLS"))
			}
		}
	}
	if _, _, err := net.SplitHostPort(config.Addr); err != nil {
		errs = append(errs, fmt.Errorf("OMAI_EXECUTOR_ADDR: %w", err))
	}
	if (config.TLSCert == "") != (config.TLSKey == "") {
		errs = append(errs, errors.New("OMAI_EXECUTOR_TLS_CERT and OMAI_EXECUTOR_TLS_KEY must be set together"))
	}
	if config.ClientCA != "" && config.TLSCert == "" {
		errs = append(errs, errors.New("OMAI_EXECUTOR_CLIENT_CA requires TLS certificate and key"))
	}
	if config.Environment == "production" {
		if config.AllowedTenant == "" || config.ExpectedWorkspaceID == "" {
			errs = append(errs, errors.New("production executor requires one tenant and workspace identity"))
		}
		if config.TLSCert == "" || config.ClientCA == "" {
			errs = append(errs, errors.New("production executor requires mutual TLS"))
		}
		if config.AllowInsecure {
			errs = append(errs, errors.New("production executor cannot allow insecure transport"))
		}
	} else if config.TLSCert == "" && !config.AllowInsecure && !loopback(config.Addr) {
		errs = append(errs, errors.New("cleartext executor listener must be loopback or explicitly allowed in development"))
	}
	if len(errs) > 0 {
		return config, errors.Join(errs...)
	}
	return config, nil
}

func contained(root, candidate string) bool {
	if root == "" || candidate == "" {
		return false
	}
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (c Config) TLSConfig() (*tls.Config, error) {
	if c.TLSCert == "" {
		return nil, nil
	}
	certificate, err := tls.LoadX509KeyPair(c.TLSCert, c.TLSKey)
	if err != nil {
		return nil, fmt.Errorf("load executor TLS certificate: %w", err)
	}
	config := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		NextProtos:   []string{"h2", "http/1.1"},
	}
	if c.ClientCA == "" {
		return config, nil
	}
	data, err := os.ReadFile(c.ClientCA)
	if err != nil {
		return nil, fmt.Errorf("read executor client CA: %w", err)
	}
	clients := x509.NewCertPool()
	if !clients.AppendCertsFromPEM(data) {
		return nil, errors.New("executor client CA contains no certificates")
	}
	config.ClientCAs = clients
	config.ClientAuth = tls.RequireAndVerifyClientCert
	return config, nil
}

func environment(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func positiveInteger(name string, fallback int, errs *[]error) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		*errs = append(*errs, fmt.Errorf("%s must be a positive integer", name))
		return fallback
	}
	return parsed
}

func boolean(name string, fallback bool, errs *[]error) bool {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s must be true or false", name))
		return fallback
	}
	return parsed
}

func duration(name string, fallback time.Duration, errs *[]error) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		*errs = append(*errs, fmt.Errorf("%s must be a positive duration", name))
		return fallback
	}
	return parsed
}

func stringList(name string, errs *[]error) []string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	var values []string
	if err := decoder.Decode(&values); err != nil || len(values) > 32 {
		*errs = append(*errs, fmt.Errorf("%s must be a JSON array of at most 32 strings", name))
		return nil
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		*errs = append(*errs, fmt.Errorf("%s must contain one JSON array", name))
		return nil
	}
	for _, value := range values {
		if len(value) > 64<<10 || strings.ContainsRune(value, '\x00') {
			*errs = append(*errs, fmt.Errorf("%s contains an invalid argument", name))
			return nil
		}
	}
	return values
}

func loopback(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
