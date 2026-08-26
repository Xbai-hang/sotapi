package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"reflect"
	"sync"

	"github.com/Xbai-hang/sotapi/internal/availability"
	"github.com/Xbai-hang/sotapi/internal/channel/telegram"
	"github.com/Xbai-hang/sotapi/internal/completion"
	"github.com/Xbai-hang/sotapi/internal/config"
	openaiAuth "github.com/Xbai-hang/sotapi/internal/protocol/openai/auth"
	"github.com/Xbai-hang/sotapi/internal/protocol/openai/chatcompletions"
	openaiModels "github.com/Xbai-hang/sotapi/internal/protocol/openai/models"
	"github.com/Xbai-hang/sotapi/internal/stats"
)

type runtimeInstance struct {
	config         config.Config
	logger         *slog.Logger
	server         *http.Server
	telegramClient *telegram.Client
	statistics     *stats.Store

	cancel  context.CancelCauseFunc
	errors  chan error
	done    chan struct{}
	started bool
}

func run(ctx context.Context, configPath string) error {
	cfg, err := loadRuntimeConfig(configPath)
	if err != nil {
		return err
	}

	watchCtx, stopWatcher := context.WithCancel(ctx)
	defer stopWatcher()
	watchEvents, err := config.Watch(watchCtx, configPath)
	if err != nil {
		return err
	}

	logger := slog.Default()
	active, err := newRuntimeInstance(cfg, logger)
	if err != nil {
		return err
	}
	if err := active.start(ctx); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return active.stop(context.Cause(ctx))
		case runtimeError := <-active.errors:
			return errors.Join(runtimeError, active.stop(runtimeError))
		case event, ok := <-watchEvents:
			if !ok {
				if ctx.Err() != nil {
					return active.stop(context.Cause(ctx))
				}
				watcherError := errors.New("config: file watcher stopped unexpectedly")
				return errors.Join(watcherError, active.stop(watcherError))
			}
			if event.Err != nil {
				logger.Warn("configuration watcher error", "error", event.Err)
				continue
			}

			candidateConfig, err := loadRuntimeConfig(configPath)
			if err != nil {
				logger.Warn("configuration reload rejected", "error", err)
				continue
			}
			if reflect.DeepEqual(candidateConfig, active.config) {
				logger.Debug("configuration unchanged")
				continue
			}

			candidate, err := newRuntimeInstance(candidateConfig, logger)
			if err != nil {
				logger.Warn("configuration reload rejected", "error", err)
				continue
			}

			logger.Info("configuration reload started")
			if err := active.stop(completion.ErrServiceReloading); err != nil {
				logger.Warn("runtime stopped with errors during configuration reload", "error", err)
			}
			if ctx.Err() != nil {
				return nil
			}
			if err := candidate.start(ctx); err != nil {
				logger.Error("configuration reload failed; restoring previous configuration", "error", err)
				rollback, rollbackError := newRuntimeInstance(active.config, logger)
				if rollbackError == nil {
					rollbackError = rollback.start(ctx)
				}
				if rollbackError != nil {
					return errors.Join(
						fmt.Errorf("configuration reload: %w", err),
						fmt.Errorf("restore previous runtime: %w", rollbackError),
					)
				}
				active = rollback
				continue
			}

			active = candidate
			logger.Info("configuration reload completed")
		}
	}
}

func newRuntimeInstance(cfg config.Config, logger *slog.Logger) (*runtimeInstance, error) {
	if logger == nil {
		logger = slog.Default()
	}
	authenticator, err := openaiAuth.New(cfg.Auth.Mode, cfg.Auth.APIKeys)
	if err != nil {
		return nil, err
	}
	router, err := buildRouter(cfg)
	if err != nil {
		return nil, err
	}
	statistics, err := stats.NewStore(cfg.Human.AutoOffline.AfterMissedReplies)
	if err != nil {
		return nil, err
	}
	state, err := availability.NewStore(buildRoutingUsers(cfg), availability.Config{
		Enabled:            cfg.Human.AutoOffline.Enabled,
		AfterMissedReplies: cfg.Human.AutoOffline.AfterMissedReplies,
	})
	if err != nil {
		return nil, err
	}
	fallback, err := completion.NewTemplateFallback(cfg.Fallback.Template)
	if err != nil {
		return nil, err
	}

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
		return nil, err
	}
	service, err := completion.NewService(router, telegramClient, statistics, state, fallback, completion.ServiceConfig{
		RequestTimeout:    cfg.Human.ResponseTimeout,
		ReasoningTemplate: cfg.Human.ReasoningTemplate,
	})
	if err != nil {
		return nil, err
	}
	forwarder.service = service

	chatHandler, err := chatcompletions.NewHandler(service, chatcompletions.HandlerConfig{
		Authenticator:     authenticator,
		MaxBodyBytes:      cfg.Server.MaxBodyBytes,
		KeepAliveInterval: cfg.Server.StreamKeepAlive,
	})
	if err != nil {
		return nil, err
	}
	modelIDs := make([]string, 0, len(cfg.Models))
	for _, model := range cfg.Models {
		modelIDs = append(modelIDs, model.ID)
	}
	modelsHandler, err := openaiModels.NewHandler(authenticator, modelIDs)
	if err != nil {
		return nil, err
	}
	httpHandler, err := buildHTTPHandler(chatHandler, modelsHandler, telegramClient)
	if err != nil {
		return nil, err
	}

	return &runtimeInstance{
		config:         cfg,
		logger:         logger,
		telegramClient: telegramClient,
		statistics:     statistics,
		server: &http.Server{
			Addr:              cfg.Server.ListenAddress,
			Handler:           httpHandler,
			ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
			IdleTimeout:       cfg.Server.IdleTimeout,
		},
	}, nil
}

func (r *runtimeInstance) start(parent context.Context) error {
	if r.started {
		return errors.New("runtime: instance is already started")
	}

	listener, err := net.Listen("tcp", r.config.Server.ListenAddress)
	if err != nil {
		return fmt.Errorf("HTTP server listen: %w", err)
	}
	runCtx, cancel := context.WithCancelCause(parent)
	if err := r.telegramClient.Configure(runCtx); err != nil {
		cancel(err)
		_ = listener.Close()
		return fmt.Errorf("telegram updates: %w", err)
	}

	r.cancel = cancel
	r.errors = make(chan error, 2)
	r.done = make(chan struct{})
	r.server.BaseContext = func(net.Listener) context.Context { return runCtx }
	r.started = true

	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		r.logger.Info("sotapi listening",
			"address", listener.Addr().String(),
			"base_url", r.config.Server.BaseURL,
			"telegram_update_mode", r.config.Telegram.UpdateMode,
		)
		if err := r.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			r.reportError(runCtx, fmt.Errorf("HTTP server: %w", err))
		}
	}()
	go func() {
		defer workers.Done()
		if err := r.telegramClient.Receive(runCtx); err != nil {
			r.reportError(runCtx, fmt.Errorf("telegram updates: %w", err))
		}
	}()
	go func() {
		workers.Wait()
		close(r.done)
	}()
	return nil
}

func (r *runtimeInstance) stop(cause error) error {
	if !r.started {
		return nil
	}
	r.cancel(cause)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), r.config.Server.ShutdownTimeout)
	defer shutdownCancel()
	shutdownError := r.server.Shutdown(shutdownCtx)
	if shutdownError != nil {
		_ = r.server.Close()
	}
	<-r.done
	r.started = false
	logStatistics(r.logger, r.statistics.All())
	if shutdownError != nil {
		return fmt.Errorf("HTTP server shutdown: %w", shutdownError)
	}
	return nil
}

func (r *runtimeInstance) reportError(ctx context.Context, err error) {
	select {
	case r.errors <- err:
	case <-ctx.Done():
	}
}
