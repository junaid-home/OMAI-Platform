package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpchealth"
	"connectrpc.com/grpcreflect"
	"github.com/omai/backend/gen/go/omai/executor/v1/executorv1connect"
	"github.com/omai/backend/gen/go/uab/v1/uabv1connect"
	commandadapter "github.com/omai/backend/internal/adapter/command"
	"github.com/omai/backend/internal/adapter/harness"
	"github.com/omai/backend/internal/adapter/osfs"
	processadapter "github.com/omai/backend/internal/adapter/process"
	"github.com/omai/backend/internal/platform/executorconfig"
	"github.com/omai/backend/internal/transport/executorapi"
	"github.com/omai/backend/internal/transport/runtimeapi"
)

const maxAuthorizationBytes = 16 << 10

var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("OMAI workspace executor stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	config, err := executorconfig.Load()
	if err != nil {
		return err
	}
	workspaces := osfs.NewWorkspaces([]string{config.WorkspaceRoot}, config.MaxFileBytes)
	processes := processadapter.New(workspaces, config.ProcessBuffer, config.MaxProcesses)
	defer processes.Close()
	commands := commandadapter.NewLocal(workspaces, config.MaxCommandOutput, config.CommandTimeout)
	service := &executorapi.Service{
		Processes: processes, Commands: commands, Workspaces: workspaces, Root: config.WorkspaceRoot,
		AllowedTenant: config.AllowedTenant, ExpectedWorkspaceID: config.ExpectedWorkspaceID,
	}

	mux := http.NewServeMux()
	path, handler := executorv1connect.NewWorkspaceExecutorServiceHandler(service, connect.WithReadMaxBytes(int(config.MaxArchiveBytes+(1<<20))))
	mux.Handle(path, handler)
	serviceNames := []string{executorv1connect.WorkspaceExecutorServiceName}
	var modelEdgeServer *http.Server
	var modelEdgeListener net.Listener
	if config.HarnessDriver != "" {
		modelEdgeListener, err = net.Listen("tcp", config.HarnessModelEdgeAddr)
		if err != nil {
			return errors.New("listen on harness model edge: " + err.Error())
		}
		modelEdgeURL := "http://" + modelEdgeListener.Addr().String()
		leases, err := harness.NewLeaseStore(modelEdgeURL, config.HarnessLeaseTTL, config.MaxProcesses)
		if err != nil {
			_ = modelEdgeListener.Close()
			return err
		}
		modelEdge, err := harness.NewModelEdge(leases, harness.ModelGatewayConfig{
			Endpoint: config.ModelGatewayEndpoint, Token: config.ModelGatewayToken, Transport: config.ModelGatewayTransport,
			CACert: config.ModelGatewayCACert, ClientCert: config.ModelGatewayClientCert,
			ClientKey: config.ModelGatewayClientKey, TLSServerName: config.ModelGatewayTLSServerName,
		}, version)
		if err != nil {
			_ = modelEdgeListener.Close()
			return err
		}
		store, err := harness.NewFileSessionStore(config.HarnessStateFile)
		if err != nil {
			_ = modelEdgeListener.Close()
			return err
		}
		driver, err := harness.NewOpenCode(harness.OpenCodeConfig{
			ID: "opencode", Command: config.HarnessCommand, CommandArgs: config.HarnessCommandArgs, Workspace: config.WorkspaceRoot,
			Home: config.HarnessHome, Version: config.HarnessVersion, AutoApprove: config.HarnessAutoApprove,
		})
		if err != nil {
			_ = modelEdgeListener.Close()
			return err
		}
		supervisor := harness.NewSupervisor(driver, harness.NewLocalRunner(config.HarnessMaxLineBytes, config.HarnessMaxStderrBytes), store, leases)
		runtimeService := &runtimeapi.Service{
			Runtime: supervisor, AllowedTenant: config.AllowedTenant,
			ExpectedWorkspaceID: config.ExpectedWorkspaceID, Root: config.WorkspaceRoot,
		}
		path, handler = uabv1connect.NewAgentRuntimeServiceHandler(runtimeService)
		mux.Handle(path, handler)
		serviceNames = append(serviceNames, uabv1connect.AgentRuntimeServiceName)
		modelEdgeServer = &http.Server{
			Handler: modelEdge.Handler(), ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 16 << 10,
		}
		slog.Info("OMAI harness driver enabled", "driver", config.HarnessDriver, "model_edge", modelEdgeListener.Addr().String())
	}
	health := grpchealth.NewStaticChecker(serviceNames...)
	path, handler = grpchealth.NewHandler(health)
	mux.Handle(path, handler)
	if config.EnableReflection {
		reflector := grpcreflect.NewStaticReflector(serviceNames...)
		path, handler = grpcreflect.NewHandlerV1(reflector)
		mux.Handle(path, handler)
		path, handler = grpcreflect.NewHandlerV1Alpha(reflector)
		mux.Handle(path, handler)
	}

	protected := bearerMiddleware(config.Token, http.MaxBytesHandler(mux, max(config.MaxBodyBytes, config.MaxArchiveBytes+(1<<20))))
	root := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && (request.URL.Path == "/livez" || request.URL.Path == "/readyz") {
			response.WriteHeader(http.StatusNoContent)
			return
		}
		protected.ServeHTTP(response, request)
	})
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(config.TLSCert == "")
	server := &http.Server{
		Addr: config.Addr, Handler: root, Protocols: protocols, ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 64 << 10,
	}
	errCh := make(chan error, 2)
	if modelEdgeServer != nil {
		go func() { errCh <- modelEdgeServer.Serve(modelEdgeListener) }()
	}
	go func() {
		if config.TLSCert == "" {
			errCh <- server.ListenAndServe()
			return
		}
		tlsConfig, err := config.TLSConfig()
		if err != nil {
			errCh <- err
			return
		}
		server.TLSConfig = tlsConfig
		errCh <- server.ListenAndServeTLS("", "")
	}()
	slog.Info("OMAI workspace executor started", "addr", config.Addr, "version", version, "workspace_id", config.ExpectedWorkspaceID)
	stop, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	select {
	case <-stop.Done():
		shutdown, done := context.WithTimeout(context.Background(), config.GracePeriod)
		defer done()
		if modelEdgeServer != nil {
			_ = modelEdgeServer.Shutdown(shutdown)
		}
		return server.Shutdown(shutdown)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		shutdown, done := context.WithTimeout(context.Background(), config.GracePeriod)
		defer done()
		_ = server.Shutdown(shutdown)
		if modelEdgeServer != nil {
			_ = modelEdgeServer.Shutdown(shutdown)
		}
		return err
	}
}

func bearerMiddleware(token string, next http.Handler) http.Handler {
	expected := []byte("Bearer " + token)
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		credential := request.Header.Get("Authorization")
		if len(credential) > maxAuthorizationBytes || subtle.ConstantTimeCompare([]byte(credential), expected) != 1 {
			response.Header().Set("WWW-Authenticate", `Bearer realm="omai-workspace-executor"`)
			http.Error(response, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(response, request)
	})
}
