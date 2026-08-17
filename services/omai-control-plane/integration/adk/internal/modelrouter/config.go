package modelrouter

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	DriverGemini                = "gemini"
	DriverOpenAIResponses       = "openai-responses"
	DriverOpenAIChatCompletions = "openai-chat-completions"
)

var (
	providerIDPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	environmentPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,127}$`)
)

type Route struct {
	ProviderID string `json:"provider_id"`
	ModelID    string `json:"model_id"`
}

type Provider struct {
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Driver         string        `json:"driver"`
	APIKeyEnv      string        `json:"api_key_env"`
	BaseURL        string        `json:"base_url,omitempty"`
	DefaultModel   string        `json:"default_model"`
	ModelPrefixes  []string      `json:"model_prefixes,omitempty"`
	AllowAllModels bool          `json:"allow_all_models,omitempty"`
	AllowAnonymous bool          `json:"allow_anonymous,omitempty"`
	Enabled        bool          `json:"enabled"`
	RequestTimeout time.Duration `json:"-"`
	Timeout        string        `json:"request_timeout,omitempty"`
}

type Config struct {
	SchemaVersion   string     `json:"schema_version"`
	Default         Route      `json:"default"`
	Providers       []Provider `json:"providers"`
	MaxCachedModels int        `json:"max_cached_models,omitempty"`
}

func Load(path string) (Config, error) {
	if strings.TrimSpace(path) == "" {
		return Config{}, errors.New("OMAI_ADK_PROVIDERS_FILE is required")
	}
	// #nosec G304,G703 -- provider configuration is an operator-owned startup path.
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read provider configuration: %w", err)
	}
	var cfg Config
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode provider configuration: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("decode provider configuration: unexpected trailing JSON value")
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.SchemaVersion != "1" {
		return fmt.Errorf("unsupported provider schema version %q", c.SchemaVersion)
	}
	if c.MaxCachedModels == 0 {
		c.MaxCachedModels = 256
	}
	if c.MaxCachedModels < 1 || c.MaxCachedModels > 2048 {
		return errors.New("max_cached_models must be between 1 and 2048")
	}
	seen := make(map[string]struct{}, len(c.Providers))
	for index := range c.Providers {
		provider := &c.Providers[index]
		if !providerIDPattern.MatchString(provider.ID) {
			return fmt.Errorf("provider %d has an invalid id", index)
		}
		if _, exists := seen[provider.ID]; exists {
			return fmt.Errorf("duplicate provider %q", provider.ID)
		}
		seen[provider.ID] = struct{}{}
		if strings.TrimSpace(provider.Name) == "" {
			return fmt.Errorf("provider %q requires a name", provider.ID)
		}
		if provider.Driver != DriverGemini && provider.Driver != DriverOpenAIResponses && provider.Driver != DriverOpenAIChatCompletions {
			return fmt.Errorf("provider %q has unsupported driver %q", provider.ID, provider.Driver)
		}
		if !validModelID(provider.DefaultModel) {
			return fmt.Errorf("provider %q has an invalid default model", provider.ID)
		}
		if provider.APIKeyEnv != "" && !environmentPattern.MatchString(provider.APIKeyEnv) {
			return fmt.Errorf("provider %q has an invalid api_key_env", provider.ID)
		}
		if provider.Driver == DriverGemini && provider.BaseURL != "" {
			return fmt.Errorf("provider %q cannot override the Gemini endpoint", provider.ID)
		}
		if provider.Driver == DriverOpenAIResponses || provider.Driver == DriverOpenAIChatCompletions {
			if err := validateBaseURL(provider.BaseURL, provider.AllowAnonymous); err != nil {
				return fmt.Errorf("provider %q: %w", provider.ID, err)
			}
		}
		if provider.AllowAnonymous && provider.APIKeyEnv != "" {
			return fmt.Errorf("provider %q cannot combine allow_anonymous and api_key_env", provider.ID)
		}
		if !provider.AllowAnonymous && provider.APIKeyEnv == "" {
			return fmt.Errorf("provider %q requires api_key_env", provider.ID)
		}
		for _, prefix := range provider.ModelPrefixes {
			if prefix == "" || strings.TrimSpace(prefix) != prefix || len(prefix) > 128 {
				return fmt.Errorf("provider %q has an invalid model prefix", provider.ID)
			}
		}
		if !provider.AllowAllModels && !provider.allows(provider.DefaultModel) {
			return fmt.Errorf("provider %q default model is outside its allowlist", provider.ID)
		}
		provider.RequestTimeout = 10 * time.Minute
		if provider.Timeout != "" {
			timeout, err := time.ParseDuration(provider.Timeout)
			if err != nil || timeout < time.Second || timeout > 30*time.Minute {
				return fmt.Errorf("provider %q request_timeout must be between 1s and 30m", provider.ID)
			}
			provider.RequestTimeout = timeout
		}
	}
	if c.Default.ProviderID == "" || c.Default.ModelID == "" {
		return errors.New("default provider_id and model_id are required")
	}
	provider, ok := c.provider(c.Default.ProviderID)
	if !ok || !provider.Enabled {
		return fmt.Errorf("default provider %q is not enabled", c.Default.ProviderID)
	}
	if !provider.allows(c.Default.ModelID) {
		return fmt.Errorf("default model %q is outside provider %q allowlist", c.Default.ModelID, provider.ID)
	}
	return nil
}

func (c Config) provider(id string) (Provider, bool) {
	for _, provider := range c.Providers {
		if provider.ID == id {
			return provider, true
		}
	}
	return Provider{}, false
}

func (p Provider) allows(modelID string) bool {
	if !validModelID(modelID) {
		return false
	}
	if p.AllowAllModels {
		return true
	}
	for _, prefix := range p.ModelPrefixes {
		if strings.HasPrefix(modelID, prefix) {
			return true
		}
	}
	return modelID == p.DefaultModel
}

func validModelID(value string) bool {
	if value == "" || len(value) > 256 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character <= 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validateBaseURL(raw string, anonymous bool) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("base_url must be an absolute URL without credentials, query, or fragment")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme != "http" || !loopbackHost(parsed.Hostname()) {
		return errors.New("base_url must use HTTPS; HTTP is allowed only for loopback development endpoints")
	}
	if !anonymous {
		return errors.New("loopback HTTP providers must explicitly set allow_anonymous")
	}
	return nil
}

func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
