package harness

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"connectrpc.com/connect"
	"github.com/omai/backend/gen/go/uab/v1/uabv1connect"
)

type ModelGatewayConfig struct {
	Endpoint      string
	Token         string
	Transport     string
	CACert        string
	ClientCert    string
	ClientKey     string
	TLSServerName string
}

func newModelGatewayClient(config ModelGatewayConfig) (uabv1connect.ModelGatewayServiceClient, error) {
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return nil, errors.New("model gateway endpoint must be an absolute HTTP(S) URL")
	}
	if len(strings.TrimSpace(config.Token)) < 32 {
		return nil, errors.New("model gateway token must contain at least 32 characters")
	}
	if config.Transport == "" {
		config.Transport = "connect"
	}
	if config.Transport != "connect" && config.Transport != "grpc" {
		return nil, errors.New("model gateway transport must be connect or grpc")
	}
	if (config.ClientCert == "") != (config.ClientKey == "") {
		return nil, errors.New("model gateway client certificate and key must be set together")
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("unsupported default HTTP transport %T", http.DefaultTransport)
	}
	transport := base.Clone()
	if config.Transport == "grpc" && endpoint.Scheme == "http" {
		transport.Protocols = new(http.Protocols)
		transport.Protocols.SetUnencryptedHTTP2(true)
	}
	if endpoint.Scheme == "https" {
		tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: config.TLSServerName}
		if config.CACert != "" {
			// #nosec G304 -- certificate paths are operator-owned executor configuration.
			data, err := os.ReadFile(config.CACert)
			if err != nil {
				return nil, fmt.Errorf("read model gateway CA: %w", err)
			}
			roots := x509.NewCertPool()
			if !roots.AppendCertsFromPEM(data) {
				return nil, errors.New("model gateway CA contains no certificates")
			}
			tlsConfig.RootCAs = roots
		}
		if config.ClientCert != "" {
			certificate, err := tls.LoadX509KeyPair(config.ClientCert, config.ClientKey)
			if err != nil {
				return nil, fmt.Errorf("load model gateway client certificate: %w", err)
			}
			tlsConfig.Certificates = []tls.Certificate{certificate}
		}
		transport.TLSClientConfig = tlsConfig
	}
	client := &http.Client{Transport: &modelTokenTransport{base: transport, token: strings.TrimSpace(config.Token)}}
	options := make([]connect.ClientOption, 0, 1)
	if config.Transport == "grpc" {
		options = append(options, connect.WithGRPC())
	}
	return uabv1connect.NewModelGatewayServiceClient(client, strings.TrimRight(config.Endpoint, "/"), options...), nil
}

type modelTokenTransport struct {
	base  http.RoundTripper
	token string
}

func (t *modelTokenTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+t.token)
	clone.Header.Set("X-OMAI-Tenant-ID", "harness-model-edge")
	clone.Header.Set("X-OMAI-Actor-ID", "workspace-executor")
	return t.base.RoundTrip(clone)
}
