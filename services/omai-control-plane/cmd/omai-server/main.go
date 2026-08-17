package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpchealth"
	"connectrpc.com/grpcreflect"
	platformv1connect "github.com/omai/backend/gen/go/omai/platform/v1/platformv1connect"
	uabv1connect "github.com/omai/backend/gen/go/uab/v1/uabv1connect"
	commandadapter "github.com/omai/backend/internal/adapter/command"
	"github.com/omai/backend/internal/adapter/gitcli"
	"github.com/omai/backend/internal/adapter/lsp"
	"github.com/omai/backend/internal/adapter/memory"
	"github.com/omai/backend/internal/adapter/modelcatalog"
	"github.com/omai/backend/internal/adapter/osfs"
	"github.com/omai/backend/internal/adapter/preview"
	processadapter "github.com/omai/backend/internal/adapter/process"
	"github.com/omai/backend/internal/adapter/projectdetector"
	"github.com/omai/backend/internal/adapter/redisstore"
	runtimeadapter "github.com/omai/backend/internal/adapter/runtime"
	"github.com/omai/backend/internal/application"
	"github.com/omai/backend/internal/platform/auth"
	"github.com/omai/backend/internal/platform/config"
	"github.com/omai/backend/internal/platform/httpguard"
	"github.com/omai/backend/internal/platform/telemetry"
	"github.com/omai/backend/internal/port"
	reflectionregistry "github.com/omai/backend/internal/reflection"
	"github.com/omai/backend/internal/transport/connectapi"
	"github.com/omai/backend/internal/transport/localinvoke"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("OMAI stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	tools, err := reflectionregistry.Build()
	if err != nil {
		return err
	}
	invoker := localinvoke.New(cfg.MaxBodyBytes)
	var voiceLeases port.VoiceLeaseStore = memory.NewVoiceLeases()
	var voiceDispatches port.VoiceDispatchStore = memory.NewVoiceDispatches()
	var sessions port.SessionRepository = memory.NewSessions()
	var projects port.ProjectRepository = memory.NewProjects()
	var conversations port.ConversationRepository = memory.NewConversations()
	var events port.EventRepository = memory.NewEvents(cfg.EventBuffer)
	var mcpRepository port.MCPRepository = memory.NewMCP()
	var permissions port.PermissionRepository = memory.NewPermissions()
	var questions port.QuestionRepository = memory.NewQuestions()
	var turnLeases port.TurnLeaseStore
	if cfg.RedisAddr != "" {
		redisClient := redisstore.NewClient(cfg.RedisAddr, cfg.RedisPassword, 0)
		if err := redisClient.Ping(context.Background()).Err(); err != nil {
			return err
		}
		defer redisClient.Close()
		voiceLeases = redisstore.NewVoiceLeases(redisClient, "")
		voiceDispatches = redisstore.NewVoiceDispatches(redisClient, "")
		sessions = redisstore.NewSessions(redisClient, "")
		projects = redisstore.NewProjects(redisClient, "")
		conversations = redisstore.NewConversations(redisClient, "")
		events = redisstore.NewEvents(redisClient, "", cfg.EventBuffer)
		mcpRepository = redisstore.NewMCP(redisClient, "")
		permissions = redisstore.NewPermissions(redisClient, "")
		questions = redisstore.NewQuestions(redisClient, "")
		turnLeases = redisstore.NewTurnLeases(redisClient, "")
		slog.Info("durable platform stores enabled", "backend", "redis")
	}
	workspaces := osfs.NewWorkspaces(cfg.WorkspaceRoots, cfg.MaxFileBytes)
	var workspaceRepository port.WorkspaceRepository = workspaces
	var processes port.ProcessManager
	var commands port.WorkspaceCommandRunner
	closeProcesses := func() error { return nil }
	if cfg.ExecutorEndpoint != "" {
		remoteProcesses, remoteErr := processadapter.NewRemote(workspaces, processadapter.RemoteConfig{
			Endpoint: cfg.ExecutorEndpoint, ControlRoot: cfg.ExecutorControlRoot, Token: cfg.ExecutorToken, Transport: cfg.ExecutorTransport,
			CACert: cfg.ExecutorCACert, ClientCert: cfg.ExecutorClientCert, ClientKey: cfg.ExecutorClientKey,
			TLSServerName: cfg.ExecutorTLSServerName,
		})
		if remoteErr != nil {
			return remoteErr
		}
		processes = remoteProcesses
		commands = remoteProcesses
		workspaceRepository = remoteProcesses.WorkspaceRepository()
		slog.Info("workspace processes delegated to isolated Go executor", "endpoint", cfg.ExecutorEndpoint, "transport", cfg.ExecutorTransport)
	} else {
		localProcesses := processadapter.New(workspaces, cfg.ProcessBuffer, cfg.MaxProcesses)
		processes = localProcesses
		commands = commandadapter.NewLocal(workspaces, cfg.MaxCommandOutput, 2*time.Minute)
		closeProcesses = localProcesses.Close
		slog.Warn("using in-process workspace executor; development only")
	}
	defer closeProcesses()
	voiceControl := application.NewVoiceControl(voiceLeases, voiceDispatches, workspaceRepository, tools, invoker, cfg.VoiceTicketTTL, cfg.VoiceLeaseTTL, cfg.VoiceMaxSessions)
	gitRepository := gitcli.New(workspaceRepository, commands, cfg.MaxCommandOutput)
	languageServers := lsp.NewRegistry()
	previewGateway, err := preview.New(cfg.PreviewBaseURL)
	if err != nil {
		return err
	}
	previewPublisher, err := preview.NewPublisher(preview.PublisherConfig{PublicBaseURL: cfg.PreviewPublicBaseURL, PublicURLTemplate: cfg.PreviewURLTemplate})
	if err != nil {
		return err
	}
	catalog, err := application.LoadCatalog(cfg.ModelCatalogFile)
	if err != nil {
		return err
	}
	modelRefreshContext, stopModelRefresh := context.WithCancel(context.Background())
	defer stopModelRefresh()
	if cfg.ModelSyncFile != "" {
		modelSyncConfig, err := modelcatalog.LoadFile(cfg.ModelSyncFile)
		if err != nil {
			return err
		}
		modelRefresher := modelcatalog.New(modelSyncConfig)
		snapshotLoaded := false
		if modelSyncConfig.SourceFile != "" {
			snapshot, snapshotErr := modelRefresher.LoadSnapshot()
			if snapshotErr != nil {
				if len(catalog.ModelSnapshot()) == 0 {
					return snapshotErr
				}
				slog.Warn("vendored models.dev snapshot failed; using normalized fallback", "error", snapshotErr)
			} else {
				catalog.Replace(snapshot)
				snapshotLoaded = true
				slog.Info("loaded vendored models.dev catalog", "providers", len(catalog.ProviderSnapshot()), "models", len(catalog.ModelSnapshot()))
			}
		}
		if !snapshotLoaded {
			refreshed, refreshErr := modelRefresher.Refresh(modelRefreshContext)
			if refreshErr != nil {
				if len(catalog.ModelSnapshot()) == 0 {
					return refreshErr
				}
				slog.Warn("initial model catalog refresh failed; using local snapshot", "error", refreshErr)
			} else {
				catalog.Replace(refreshed)
			}
		}
		go modelRefresher.Run(modelRefreshContext, catalog, snapshotLoaded)
	}
	runtimes := runtimeadapter.NewRegistry()
	if cfg.EnableDemoRuntime {
		if err := runtimes.Register(runtimeadapter.NewDemo()); err != nil {
			return err
		}
	}
	if cfg.RuntimesFile != "" {
		remoteConfigs, err := runtimeadapter.LoadFile(cfg.RuntimesFile)
		if err != nil {
			return err
		}
		for _, remoteConfig := range remoteConfigs {
			if cfg.Environment == "production" {
				if err := remoteConfig.ValidateProduction(); err != nil {
					return err
				}
			}
			remote, err := runtimeadapter.NewRemote(remoteConfig, cfg.RuntimeHealthTimeout)
			if err != nil {
				return err
			}
			if err := runtimes.Register(remote); err != nil {
				return err
			}
		}
	}
	previewManager, err := application.NewPreviewManager(
		workspaceRepository, processes, commands, projectdetector.New(), previewPublisher, events,
		application.PreviewConfig{
			BindHost: cfg.PreviewBindHost, RuntimeHost: cfg.PreviewRuntimeHost,
			Preparation:        application.PreviewPreparationPolicy(cfg.PreviewPreparation),
			PreparationTimeout: cfg.PreviewPrepareTimeout, StartupTimeout: cfg.PreviewStartupTimeout, IdleTimeout: cfg.PreviewIdleTimeout,
		},
	)
	if err != nil {
		return err
	}
	previewContext, stopPreviews := context.WithCancel(context.Background())
	defer stopPreviews()
	previewManager.StartReaper(previewContext)
	orchestrator := application.NewOrchestrator(runtimes, workspaceRepository, sessions, conversations, events)
	orchestrator.UseTurnLeases(turnLeases)
	interactions := application.NewInteractions(sessions, permissions, questions, events)
	platform := application.NewPlatform(projects, sessions, conversations, events, workspaceRepository, runtimes, orchestrator, catalog)
	platform.UseInteractions(interactions)
	services := &connectapi.Services{Version: version, Orchestrator: orchestrator, Runtimes: runtimes, Sessions: sessions, Conversations: conversations, Events: events, Workspaces: workspaceRepository, Git: gitRepository, Processes: processes, LSP: languageServers, MCP: mcpRepository, Preview: previewGateway, Catalog: catalog, Voice: voiceControl, Tools: tools}

	mux := http.NewServeMux()
	register := func(path string, handler http.Handler) { mux.Handle(path, handler) }
	path, handler := uabv1connect.NewControlPlaneServiceHandler(services)
	register(path, handler)
	path, handler = platformv1connect.NewProjectServiceHandler(&connectapi.ProjectService{Platform: platform})
	register(path, handler)
	path, handler = platformv1connect.NewSessionServiceHandler(&connectapi.SessionService{Platform: platform})
	register(path, handler)
	path, handler = platformv1connect.NewPermissionServiceHandler(&connectapi.PermissionService{Interactions: interactions})
	register(path, handler)
	path, handler = platformv1connect.NewQuestionServiceHandler(&connectapi.QuestionService{Interactions: interactions})
	register(path, handler)
	path, handler = uabv1connect.NewWorkspaceGatewayServiceHandler(services)
	register(path, handler)
	path, handler = uabv1connect.NewPreviewServiceHandler(&connectapi.PreviewService{Manager: previewManager})
	register(path, handler)
	path, handler = uabv1connect.NewWorkspaceServiceHandler(services, connect.WithReadMaxBytes(int(cfg.MaxArchiveBytes+(1<<20))))
	register(path, handler)
	path, handler = uabv1connect.NewGitServiceHandler(services)
	register(path, handler)
	path, handler = uabv1connect.NewTerminalServiceHandler(&connectapi.TerminalService{Core: services})
	register(path, handler)
	path, handler = uabv1connect.NewLSPServiceHandler(&connectapi.LSPService{Core: services})
	register(path, handler)
	path, handler = uabv1connect.NewRuntimeServiceHandler(&connectapi.RuntimeService{Core: services})
	register(path, handler)
	path, handler = uabv1connect.NewConversationServiceHandler(services)
	register(path, handler)
	path, handler = uabv1connect.NewMCPServiceHandler(&connectapi.MCPService{Core: services})
	register(path, handler)
	path, handler = uabv1connect.NewModelCatalogServiceHandler(services)
	register(path, handler)
	path, handler = uabv1connect.NewToolRegistryServiceHandler(services)
	register(path, handler)
	path, handler = uabv1connect.NewVoiceControlServiceHandler(&connectapi.VoiceService{Core: services})
	register(path, handler)
	path, handler = uabv1connect.NewPortalControlServiceHandler(&connectapi.PortalService{Core: services})
	register(path, handler)
	invoker.SetHandler(mux)
	serviceNames := []string{
		platformv1connect.ProjectServiceName,
		platformv1connect.SessionServiceName,
		platformv1connect.PermissionServiceName,
		platformv1connect.QuestionServiceName,
		uabv1connect.ControlPlaneServiceName,
		uabv1connect.WorkspaceGatewayServiceName,
		uabv1connect.PreviewServiceName,
		uabv1connect.WorkspaceServiceName,
		uabv1connect.GitServiceName,
		uabv1connect.TerminalServiceName,
		uabv1connect.LSPServiceName,
		uabv1connect.RuntimeServiceName,
		uabv1connect.ConversationServiceName,
		uabv1connect.MCPServiceName,
		uabv1connect.ModelCatalogServiceName,
		uabv1connect.ToolRegistryServiceName,
		uabv1connect.VoiceControlServiceName,
		uabv1connect.PortalControlServiceName,
	}
	checker := grpchealth.NewStaticChecker(serviceNames...)
	path, handler = grpchealth.NewHandler(checker)
	register(path, handler)
	if cfg.EnableReflection {
		reflector := grpcreflect.NewStaticReflector(serviceNames...)
		path, handler = grpcreflect.NewHandlerV1(reflector)
		register(path, handler)
		path, handler = grpcreflect.NewHandlerV1Alpha(reflector)
		register(path, handler)
	}
	metrics := telemetry.New()
	mux.HandleFunc("/livez", func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/readyz", func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusNoContent) })
	authenticator := auth.New(cfg.DevToken, cfg.ServiceToken, cfg.JWTSecret, cfg.JWTIssuer, cfg.JWTAudience, tools)
	handler = httpguard.New(cfg.AllowedOrigins, cfg.RatePerSecond, cfg.RateBurst, metrics).Middleware(authenticator.Middleware(http.MaxBytesHandler(mux, max(cfg.MaxBodyBytes, cfg.MaxArchiveBytes+(1<<20)))))
	handler = previewPublisher.Wrap(handler)
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(cfg.TLSCert == "")
	server := &http.Server{Addr: cfg.Addr, Handler: handler, Protocols: protocols, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 1 << 20}
	metricsMux := http.NewServeMux()
	metricsMux.HandleFunc("/metrics", metrics.Handler)
	metricsServer := &http.Server{Addr: cfg.MetricsAddr, Handler: metricsMux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10}
	errCh := make(chan error, 2)
	go func() {
		if cfg.TLSCert != "" {
			tlsConfig, err := cfg.TLSConfig()
			if err != nil {
				errCh <- err
				return
			}
			server.TLSConfig = tlsConfig
			errCh <- server.ListenAndServeTLS("", "")
		} else {
			errCh <- server.ListenAndServe()
		}
	}()
	go func() { errCh <- metricsServer.ListenAndServe() }()
	stop, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	select {
	case <-stop.Done():
		shutdown, done := context.WithTimeout(context.Background(), cfg.GracePeriod)
		defer done()
		return errors.Join(server.Shutdown(shutdown), metricsServer.Shutdown(shutdown))
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
