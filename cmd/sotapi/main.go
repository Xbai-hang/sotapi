package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/Xbai-hang/sotapi/internal/channel/telegram"
	"github.com/Xbai-hang/sotapi/internal/completion"
	"github.com/Xbai-hang/sotapi/internal/config"
	openaiAuth "github.com/Xbai-hang/sotapi/internal/protocol/openai/auth"
	"github.com/Xbai-hang/sotapi/internal/protocol/openai/chatcompletions"
	openaiModels "github.com/Xbai-hang/sotapi/internal/protocol/openai/models"
	"github.com/Xbai-hang/sotapi/internal/routing"
	"github.com/Xbai-hang/sotapi/internal/stats"
)

const healthPath = "/healthz"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	command := newRootCommand()
	if err := command.ExecuteContext(ctx); err != nil {
		slog.Error("sotapi stopped", "error", err)
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	configPath := defaultConfigPath()
	command := &cobra.Command{
		Use:          "sotapi",
		Short:        "Expose human answers through an OpenAI-compatible API",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(command *cobra.Command, _ []string) error {
			return run(command.Context(), configPath)
		},
	}
	command.PersistentFlags().StringVarP(&configPath, "config", "c", configPath, "path to the YAML configuration file")
	command.AddCommand(newConfigCommand(&configPath))
	return command
}

func newConfigCommand(configPath *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "config",
		Short: "Validate configuration",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(&cobra.Command{
		Use:   "validate",
		Short: "Validate the SotAPI YAML configuration",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			_, err := loadRuntimeConfig(*configPath)
			return err
		},
	})
	return command
}

func loadRuntimeConfig(configPath string) (config.Config, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return config.Config{}, err
	}
	if _, err := buildRouter(cfg); err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

func defaultConfigPath() string {
	if value := os.Getenv("SOTAPI_CONFIG"); value != "" {
		return value
	}
	return "configs/config.yaml"
}

func run(ctx context.Context, configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	authenticator, err := openaiAuth.New(cfg.Auth.Mode, cfg.Auth.APIKeys)
	if err != nil {
		return err
	}

	router, err := buildRouter(cfg)
	if err != nil {
		return err
	}
	statistics, err := stats.NewStore(cfg.UnansweredThreshold)
	if err != nil {
		return err
	}

	logger := slog.Default()
	forwarder := &replyForwarder{}
	telegramClient, err := telegram.NewClient(telegram.Config{
		BotToken:           cfg.Telegram.BotToken,
		APIBaseURL:         cfg.Telegram.APIBaseURL,
		UpdateMode:         cfg.Telegram.UpdateMode,
		DropPendingUpdates: cfg.Telegram.DropPendingUpdates,
		PollTimeout:        cfg.Telegram.PollTimeout,
		RetryInterval:      cfg.Telegram.RetryInterval,
		WebhookURL:         cfg.Telegram.Webhook.URL,
		WebhookSecretToken: cfg.Telegram.Webhook.SecretToken,
	}, nil, forwarder, logger)
	if err != nil {
		return err
	}
	service, err := completion.NewService(router, telegramClient, statistics, completion.ServiceConfig{
		RequestTimeout:    cfg.RequestTimeout,
		ReasoningTemplate: cfg.ReasoningTemplate,
	})
	if err != nil {
		return err
	}
	forwarder.service = service

	handler, err := chatcompletions.NewHandler(service, chatcompletions.HandlerConfig{
		Authenticator:     authenticator,
		MaxBodyBytes:      cfg.Server.MaxBodyBytes,
		KeepAliveInterval: cfg.Server.StreamKeepAlive,
	})
	if err != nil {
		return err
	}
	modelIDs := make([]string, 0, len(cfg.Models))
	for _, model := range cfg.Models {
		modelIDs = append(modelIDs, model.ID)
	}
	modelsHandler, err := openaiModels.NewHandler(authenticator, modelIDs)
	if err != nil {
		return err
	}
	httpHandler, err := buildHTTPHandler(handler, modelsHandler, telegramClient)
	if err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	server := &http.Server{
		Addr:              cfg.Server.ListenAddress,
		Handler:           httpHandler,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
		BaseContext: func(net.Listener) context.Context {
			return runCtx
		},
	}
	listener, err := net.Listen("tcp", cfg.Server.ListenAddress)
	if err != nil {
		return fmt.Errorf("HTTP server listen: %w", err)
	}

	errorsChannel := make(chan error, 2)
	go func() {
		logger.Info("sotapi listening",
			"address", listener.Addr().String(),
			"base_url", cfg.Server.BaseURL,
			"telegram_update_mode", cfg.Telegram.UpdateMode,
		)
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errorsChannel <- fmt.Errorf("HTTP server: %w", err)
		}
	}()
	go func() {
		if err := telegramClient.Run(runCtx); err != nil {
			errorsChannel <- fmt.Errorf("telegram updates: %w", err)
		}
	}()

	var runError error
	select {
	case <-ctx.Done():
	case runError = <-errorsChannel:
	}
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil && runError == nil {
		runError = fmt.Errorf("HTTP server shutdown: %w", err)
	}
	logStatistics(logger, statistics.All())
	return runError
}

func buildHTTPHandler(chatHandler, modelsHandler http.Handler, telegramClient *telegram.Client) (http.Handler, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+healthPath, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.WriteHeader(http.StatusOK)
	})
	mux.Handle(chatcompletions.Path, chatHandler)
	mux.Handle(openaiModels.Path, modelsHandler)

	webhookPath := telegramClient.WebhookPath()
	if webhookPath == "" {
		return mux, nil
	}
	if webhookPath == healthPath || webhookPath == chatcompletions.Path || webhookPath == openaiModels.Path {
		return nil, fmt.Errorf("telegram: webhook path %q conflicts with an API endpoint", webhookPath)
	}

	// Do not register a configuration-controlled path as a ServeMux pattern:
	// braces have wildcard semantics in modern Go. Dispatch by exact URL path so
	// the public webhook URL cannot accidentally capture other endpoints.
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == webhookPath {
			telegramClient.ServeHTTP(writer, request)
			return
		}
		mux.ServeHTTP(writer, request)
	}), nil
}

func buildRouter(cfg config.Config) (*routing.Router, error) {
	models := make([]routing.Model, 0, len(cfg.Models))
	for _, model := range cfg.Models {
		models = append(models, routing.Model{ID: model.ID, PoolID: model.PoolID})
	}
	pools := make([]routing.Pool, 0, len(cfg.Pools))
	for _, pool := range cfg.Pools {
		pools = append(pools, routing.Pool{ID: pool.ID, UserIDs: pool.UserIDs})
	}
	users := make([]routing.User, 0, len(cfg.Users))
	for _, user := range cfg.Users {
		users = append(users, routing.User{ID: user.ID, Channel: user.Channel, Recipient: user.Recipient})
	}
	return routing.NewRouter(models, pools, users)
}

func logStatistics(logger *slog.Logger, snapshots map[string]stats.UserStats) {
	for userID, snapshot := range snapshots {
		logger.Info("user statistics",
			"user_id", userID,
			"responded", snapshot.Responded,
			"unanswered", snapshot.Unanswered,
			"timed_out", snapshot.TimedOut,
			"canceled", snapshot.Canceled,
			"delivery_failed", snapshot.DeliveryFailed,
			"consecutive_unanswered", snapshot.ConsecutiveUnanswered,
			"average_response_time", snapshot.AverageResponseTime,
			"threshold_reached", snapshot.ThresholdReached,
		)
	}
}

type replyForwarder struct {
	service *completion.Service
}

func (f *replyForwarder) SubmitReply(requestID, content string) error {
	if f.service == nil {
		return errors.New("completion service is not ready")
	}
	return f.service.SubmitReply(requestID, content)
}
