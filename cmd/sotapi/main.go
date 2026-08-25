package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/Xbai-hang/sotapi/internal/channel/telegram"
	"github.com/Xbai-hang/sotapi/internal/completion"
	"github.com/Xbai-hang/sotapi/internal/config"
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
