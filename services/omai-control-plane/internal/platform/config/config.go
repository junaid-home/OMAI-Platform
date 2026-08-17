package config

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Environment           string
	Addr                  string
	MetricsAddr           string
	WorkspaceRoots        []string
	AllowedOrigins        []string
	DevToken              string
	ServiceToken          string
	JWTSecret             string
	JWTIssuer             string
	JWTAudience           string
	TLSCert               string
	TLSKey                string
	AllowInsecure         bool
	EnableReflection      bool
	EnableDemoRuntime     bool
	RuntimesFile          string
	ModelCatalogFile      string
	ModelSyncFile         string
	PreviewBaseURL        string
	PreviewPublicBaseURL  string
	PreviewURLTemplate    string
	PreviewBindHost       string
	PreviewRuntimeHost    string
	PreviewPreparation    string
	PreviewPrepareTimeout time.Duration
	PreviewStartupTimeout time.Duration
	PreviewIdleTimeout    time.Duration
	MaxBodyBytes          int64
	MaxFileBytes          int64
	MaxArchiveBytes       int64
	MaxCommandOutput      int
	EventBuffer           int
	ProcessBuffer         int
	MaxProcesses          int
	ExecutorEndpoint      string
	ExecutorControlRoot   string
	ExecutorToken         string
	ExecutorTransport     string
	ExecutorCACert        string
	ExecutorClientCert    string
	ExecutorClientKey     string
	ExecutorTLSServerName string
	RatePerSecond         float64
	RateBurst             float64
	GracePeriod           time.Duration
	RuntimeHealthTimeout  time.Duration
	LogLevel              string
	RedisAddr             string
	RedisPassword         string
	VoiceTicketTTL        time.Duration
	VoiceLeaseTTL         time.Duration
	VoiceMaxSessions      int
}

func Load() (Config, error) {
	var errs []error
	c := Config{
		Environment:           env("OMAI_ENV", "development"),
		Addr:                  env("OMAI_ADDR", "127.0.0.1:8787"),
		MetricsAddr:           env("OMAI_METRICS_ADDR", "127.0.0.1:9091"),
		AllowedOrigins:        csv(os.Getenv("OMAI_ALLOWED_ORIGINS")),
		DevToken:              strings.TrimSpace(os.Getenv("OMAI_DEV_TOKEN")),
		ServiceToken:          strings.TrimSpace(os.Getenv("OMAI_SERVICE_TOKEN")),
		JWTSecret:             strings.TrimSpace(os.Getenv("OMAI_JWT_HS256_SECRET")),
		JWTIssuer:             strings.TrimSpace(os.Getenv("OMAI_JWT_ISSUER")),
		JWTAudience:           strings.TrimSpace(os.Getenv("OMAI_JWT_AUDIENCE")),
		TLSCert:               strings.TrimSpace(os.Getenv("OMAI_TLS_CERT")),
		TLSKey:                strings.TrimSpace(os.Getenv("OMAI_TLS_KEY")),
		AllowInsecure:         boolEnv("OMAI_ALLOW_INSECURE", false, &errs),
		EnableReflection:      boolEnv("OMAI_ENABLE_REFLECTION", false, &errs),
		EnableDemoRuntime:     boolEnv("OMAI_ENABLE_DEMO_RUNTIME", false, &errs),
		RuntimesFile:          strings.TrimSpace(os.Getenv("OMAI_RUNTIMES_FILE")),
		ModelCatalogFile:      strings.TrimSpace(os.Getenv("OMAI_MODEL_CATALOG_FILE")),
		ModelSyncFile:         strings.TrimSpace(os.Getenv("OMAI_MODEL_SYNC_FILE")),
		PreviewBaseURL:        strings.TrimSpace(os.Getenv("OMAI_PREVIEW_BASE_URL")),
		PreviewPublicBaseURL:  strings.TrimSpace(os.Getenv("OMAI_PREVIEW_PUBLIC_BASE_URL")),
		PreviewURLTemplate:    strings.TrimSpace(os.Getenv("OMAI_PREVIEW_PUBLIC_URL_TEMPLATE")),
		PreviewBindHost:       env("OMAI_PREVIEW_BIND_HOST", "127.0.0.1"),
		PreviewRuntimeHost:    env("OMAI_PREVIEW_RUNTIME_HOST", "127.0.0.1"),
		PreviewPreparation:    env("OMAI_PREVIEW_PREPARATION", "never"),
		LogLevel:              env("OMAI_LOG_LEVEL", "info"),
		RedisAddr:             strings.TrimSpace(os.Getenv("OMAI_REDIS_ADDR")),
		RedisPassword:         os.Getenv("OMAI_REDIS_PASSWORD"),
		ExecutorEndpoint:      strings.TrimSpace(os.Getenv("OMAI_EXECUTOR_URL")),
		ExecutorControlRoot:   strings.TrimSpace(os.Getenv("OMAI_EXECUTOR_CONTROL_ROOT")),
		ExecutorToken:         strings.TrimSpace(os.Getenv("OMAI_EXECUTOR_TOKEN")),
		ExecutorTransport:     env("OMAI_EXECUTOR_TRANSPORT", "grpc"),
		ExecutorCACert:        strings.TrimSpace(os.Getenv("OMAI_EXECUTOR_CA_CERT")),
		ExecutorClientCert:    strings.TrimSpace(os.Getenv("OMAI_EXECUTOR_CLIENT_CERT")),
		ExecutorClientKey:     strings.TrimSpace(os.Getenv("OMAI_EXECUTOR_CLIENT_KEY")),
		ExecutorTLSServerName: strings.TrimSpace(os.Getenv("OMAI_EXECUTOR_TLS_SERVER_NAME")),
		VoiceTicketTTL:        durationEnv("OMAI_VOICE_TICKET_TTL", 30*time.Second, &errs),
		VoiceLeaseTTL:         durationEnv("OMAI_VOICE_LEASE_TTL", 45*time.Second, &errs),
		GracePeriod:           durationEnv("OMAI_GRACE_PERIOD", 15*time.Second, &errs),
		RuntimeHealthTimeout:  durationEnv("OMAI_RUNTIME_HEALTH_TIMEOUT", 3*time.Second, &errs),
		PreviewStartupTimeout: durationEnv("OMAI_PREVIEW_STARTUP_TIMEOUT", 45*time.Second, &errs),
		PreviewPrepareTimeout: durationEnv("OMAI_PREVIEW_PREPARATION_TIMEOUT", 5*time.Minute, &errs),
		PreviewIdleTimeout:    durationEnv("OMAI_PREVIEW_IDLE_TIMEOUT", 30*time.Minute, &errs),
	}
	c.MaxBodyBytes = int64(intEnv("OMAI_MAX_BODY_BYTES", 8<<20, &errs))
	c.MaxFileBytes = int64(intEnv("OMAI_MAX_FILE_BYTES", 4<<20, &errs))
	c.MaxArchiveBytes = int64(intEnv("OMAI_MAX_ARCHIVE_BYTES", 200<<20, &errs))
	c.MaxCommandOutput = intEnv("OMAI_MAX_COMMAND_OUTPUT", 16<<20, &errs)
	c.EventBuffer = intEnv("OMAI_EVENT_BUFFER", 10000, &errs)
	c.ProcessBuffer = intEnv("OMAI_PROCESS_BUFFER", 4<<20, &errs)
	c.MaxProcesses = intEnv("OMAI_MAX_PROCESSES_PER_TENANT", 32, &errs)
	c.RatePerSecond = float64(intEnv("OMAI_RATE_PER_SECOND", 100, &errs))
	c.RateBurst = float64(intEnv("OMAI_RATE_BURST", 200, &errs))
	c.VoiceMaxSessions = intEnv("OMAI_VOICE_MAX_SESSIONS_PER_ACTOR", 2, &errs)

	roots := csv(os.Getenv("OMAI_WORKSPACE_ROOTS"))
	if len(roots) == 0 && c.Environment == "development" {
		if cwd, err := os.Getwd(); err == nil {
			roots = []string{cwd}
		}
	}
	for _, root := range roots {
		absolute, err := filepath.Abs(root)
		if err != nil {
			errs = append(errs, fmt.Errorf("workspace root %q: %w", root, err))
			continue
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			errs = append(errs, fmt.Errorf("workspace root %q: %w", root, err))
			continue
		}
		c.WorkspaceRoots = append(c.WorkspaceRoots, filepath.Clean(resolved))
	}
	if len(c.WorkspaceRoots) == 0 {
		errs = append(errs, errors.New("OMAI_WORKSPACE_ROOTS must contain at least one existing directory"))
	}
	if c.ExecutorEndpoint != "" {
		if c.ExecutorControlRoot == "" && len(c.WorkspaceRoots) == 1 {
			c.ExecutorControlRoot = c.WorkspaceRoots[0]
		}
		if c.ExecutorControlRoot == "" {
			errs = append(errs, errors.New("OMAI_EXECUTOR_CONTROL_ROOT is required when multiple workspace roots share an executor"))
		} else {
			resolved, err := filepath.EvalSymlinks(c.ExecutorControlRoot)
			if err != nil {
				errs = append(errs, fmt.Errorf("OMAI_EXECUTOR_CONTROL_ROOT: %w", err))
			} else {
				c.ExecutorControlRoot = filepath.Clean(resolved)
				matched := false
				for _, root := range c.WorkspaceRoots {
					matched = matched || root == c.ExecutorControlRoot
				}
				if !matched {
					errs = append(errs, errors.New("OMAI_EXECUTOR_CONTROL_ROOT must equal one configured workspace root"))
				}
			}
		}
	}
	if len(c.AllowedOrigins) == 0 {
		errs = append(errs, errors.New("OMAI_ALLOWED_ORIGINS must be explicit"))
	}
	if c.Environment == "production" {
		if c.DevToken != "" {
			errs = append(errs, errors.New("OMAI_DEV_TOKEN is forbidden in production"))
		}
		if len(c.JWTSecret) < 32 && len(c.ServiceToken) < 32 {
			errs = append(errs, errors.New("production requires a strong JWT or service token"))
		}
		if c.RedisAddr == "" {
			errs = append(errs, errors.New("production requires OMAI_REDIS_ADDR for durable platform state and voice admission"))
		}
		if c.ExecutorEndpoint == "" {
			errs = append(errs, errors.New("production requires a remote workspace executor"))
		}
	} else if len(c.DevToken) < 32 && len(c.JWTSecret) < 32 && len(c.ServiceToken) < 32 {
		errs = append(errs, errors.New("configure a development, JWT, or service token with at least 32 characters"))
	}
	if c.JWTSecret != "" && (c.JWTIssuer == "" || c.JWTAudience == "") {
		errs = append(errs, errors.New("JWT issuer and audience are required when JWT validation is enabled"))
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		errs = append(errs, errors.New("OMAI_TLS_CERT and OMAI_TLS_KEY must be set together"))
	}
	if (c.ExecutorClientCert == "") != (c.ExecutorClientKey == "") {
		errs = append(errs, errors.New("OMAI_EXECUTOR_CLIENT_CERT and OMAI_EXECUTOR_CLIENT_KEY must be set together"))
	}
	if c.ExecutorTransport != "connect" && c.ExecutorTransport != "grpc" {
		errs = append(errs, errors.New("OMAI_EXECUTOR_TRANSPORT must be connect or grpc"))
	}
	if c.ExecutorEndpoint != "" {
		executorURL, err := url.Parse(c.ExecutorEndpoint)
		if err != nil || executorURL.Host == "" || executorURL.User != nil || executorURL.RawQuery != "" || executorURL.Fragment != "" || (executorURL.Scheme != "http" && executorURL.Scheme != "https") {
			errs = append(errs, errors.New("OMAI_EXECUTOR_URL must be an absolute http or https URL"))
		} else if c.Environment == "production" && executorURL.Scheme != "https" {
			errs = append(errs, errors.New("production workspace executor must use https"))
		}
		if len(c.ExecutorToken) < 32 {
			errs = append(errs, errors.New("OMAI_EXECUTOR_TOKEN must contain at least 32 characters"))
		}
		if c.Environment == "production" && (c.ExecutorCACert == "" || c.ExecutorClientCert == "") {
			errs = append(errs, errors.New("production workspace executor requires CA and client certificate configuration"))
		}
	}
	if err := validateAddress(c.Addr); err != nil {
		errs = append(errs, fmt.Errorf("OMAI_ADDR: %w", err))
	}
	if c.PreviewPublicBaseURL == "" {
		_, port, splitErr := net.SplitHostPort(c.Addr)
		if splitErr == nil {
			c.PreviewPublicBaseURL = "http://127.0.0.1:" + port
		}
	}
	if previewURL, err := url.Parse(c.PreviewPublicBaseURL); err != nil || previewURL.Hostname() == "" || previewURL.User != nil || previewURL.RawQuery != "" || previewURL.Fragment != "" || (previewURL.Scheme != "http" && previewURL.Scheme != "https") {
		errs = append(errs, errors.New("OMAI_PREVIEW_PUBLIC_BASE_URL must be an absolute http or https origin"))
	} else if c.Environment == "production" && previewURL.Scheme != "https" {
		errs = append(errs, errors.New("production preview public base URL must use https"))
	}
	if c.PreviewPreparation != "never" && c.PreviewPreparation != "auto" && c.PreviewPreparation != "always" {
		errs = append(errs, errors.New("OMAI_PREVIEW_PREPARATION must be never, auto, or always"))
	}
	if c.Environment == "production" && c.PreviewURLTemplate == "" {
		errs = append(errs, errors.New("production requires OMAI_PREVIEW_PUBLIC_URL_TEMPLATE on a separate wildcard origin"))
	}
	if c.PreviewURLTemplate != "" {
		probe, err := url.Parse(strings.Replace(c.PreviewURLTemplate, "{id}", "preview-token-marker", 1))
		if err != nil || strings.Count(c.PreviewURLTemplate, "{id}") != 1 || probe.Hostname() == "" || probe.User != nil || probe.RawQuery != "" || probe.Fragment != "" || (probe.Scheme != "http" && probe.Scheme != "https") {
			errs = append(errs, errors.New("OMAI_PREVIEW_PUBLIC_URL_TEMPLATE must be an absolute origin containing exactly one {id}"))
		} else if c.Environment == "production" && probe.Scheme != "https" {
			errs = append(errs, errors.New("production preview URL template must use https"))
		}
	}
	if err := validateAddress(c.MetricsAddr); err != nil {
		errs = append(errs, fmt.Errorf("OMAI_METRICS_ADDR: %w", err))
	}
	if c.Environment == "production" && c.TLSCert == "" && !c.AllowInsecure && !loopbackAddress(c.Addr) {
		errs = append(errs, errors.New("production cleartext listener must be loopback or explicitly allowed behind a trusted proxy"))
	}
	if len(errs) > 0 {
		return c, errors.Join(errs...)
	}
	return c, nil
}

func (c Config) TLSConfig() (*tls.Config, error) {
	if c.TLSCert == "" {
		return nil, nil
	}
	certificate, err := tls.LoadX509KeyPair(c.TLSCert, c.TLSKey)
	if err != nil {
		return nil, fmt.Errorf("load TLS certificate: %w", err)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		NextProtos:   []string{"h2", "http/1.1"},
	}, nil
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func csv(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func intEnv(name string, fallback int, errs *[]error) int {
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

func boolEnv(name string, fallback bool, errs *[]error) bool {
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

func durationEnv(name string, fallback time.Duration, errs *[]error) time.Duration {
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

func validateAddress(address string) error {
	_, _, err := net.SplitHostPort(address)
	return err
}

func loopbackAddress(address string) bool {
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
