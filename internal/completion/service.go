package completion

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Xbai-hang/sotapi/internal/routing"
)

// Delivery is an opaque Channel delivery handle used to release transport-side
// reply correlation after a request finishes.
type Delivery struct {
	ID string
}

// Deliverer sends completion tasks to a concrete human Channel.
// Implementations must make the returned delivery correlation visible before
// Deliver returns, so a fast reply cannot race registration.
type Deliverer interface {
	// Deliver sends task to target and returns its correlation handle.
	Deliver(ctx context.Context, target routing.Target, task Task) (Delivery, error)
	// Forget releases Channel-side state associated with delivery.
	Forget(delivery Delivery)
}

// Recorder consumes request lifecycle observations.
type Recorder interface {
	// Record stores one terminal request outcome.
	Record(observation Observation)
}

// StreamEmitter receives ordered protocol-neutral stream chunks.
type StreamEmitter func(chunk StreamChunk) error

// ServiceConfig contains completion lifecycle settings.
type ServiceConfig struct {
	RequestTimeout    time.Duration
	ReasoningTemplate string
}

// Service coordinates routing, Channel delivery, reply correlation and
// terminal statistics for one completion request.
type Service struct {
	router    *routing.Router
	deliverer Deliverer
	recorder  Recorder
	pending   *pendingBroker
	timeout   time.Duration
	reasoning string
}

// NewService constructs a completion Service with immutable dependencies.
func NewService(router *routing.Router, deliverer Deliverer, recorder Recorder, cfg ServiceConfig) (*Service, error) {
	if router == nil {
		return nil, errors.New("completion: router is required")
	}
	if deliverer == nil {
		return nil, errors.New("completion: deliverer is required")
	}
	if cfg.RequestTimeout <= 0 {
		return nil, errors.New("completion: request timeout must be positive")
	}

	if recorder == nil {
		recorder = discardRecorder{}
	}
	return &Service{
		router:    router,
		deliverer: deliverer,
		recorder:  recorder,
		pending:   newPendingBroker(),
		timeout:   cfg.RequestTimeout,
		reasoning: cfg.ReasoningTemplate,
	}, nil
}

// Complete waits for a human answer and returns one aggregated response.
func (s *Service) Complete(ctx context.Context, request Request) (Response, error) {
	return s.execute(ctx, request, nil)
}

// Stream emits the configured reasoning text as soon as delivery succeeds,
// followed by the complete human answer and one terminal chunk.
func (s *Service) Stream(ctx context.Context, request Request, emit StreamEmitter) error {
	if emit == nil {
		return fmt.Errorf("%w: stream emitter is required", ErrInvalidRequest)
	}
	_, err := s.execute(ctx, request, emit)
	return err
}

// SubmitReply resolves a still-pending request. Late and duplicate replies are
// rejected so they can never be delivered to a subsequent caller.
func (s *Service) SubmitReply(requestID, content string) error {
	if strings.TrimSpace(requestID) == "" || strings.TrimSpace(content) == "" {
		return fmt.Errorf("%w: reply request ID and content are required", ErrInvalidRequest)
	}
	if !s.pending.resolve(Reply{RequestID: requestID, Content: content}) {
		return fmt.Errorf("%w: %s", ErrUnknownRequest, requestID)
	}
	return nil
}

func (s *Service) execute(ctx context.Context, request Request, emit StreamEmitter) (Response, error) {
	if err := validateRequest(request); err != nil {
		return Response{}, err
	}
	if errors.Is(context.Cause(ctx), ErrServiceReloading) {
		return Response{}, ErrServiceReloading
	}
	if request.ID == "" {
		request.ID = "chatcmpl-" + rand.Text()
	}

	target, err := s.router.Resolve(request.Model)
	if err != nil {
		return Response{}, err
	}

	timeout := request.Timeout
	if timeout <= 0 {
		timeout = s.timeout
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	started := time.Now()
	replies, unregister, err := s.pending.register(request.ID)
	if err != nil {
		return Response{}, fmt.Errorf("completion: register request: %w", err)
	}
	defer unregister()

	task := Task{
		RequestID: request.ID,
		Model:     request.Model,
		Messages:  append([]Message(nil), request.Messages...),
	}
	delivery, err := s.deliverer.Deliver(requestCtx, target, task)
	if err != nil {
		if errors.Is(context.Cause(requestCtx), ErrServiceReloading) {
			s.record(request.ID, target.User.ID, OutcomeCanceled, started)
			return Response{}, ErrServiceReloading
		}
		s.record(request.ID, target.User.ID, OutcomeDeliveryFailed, started)
		return Response{}, fmt.Errorf("%w: %v", ErrDeliveryFailed, err)
	}
	defer s.deliverer.Forget(delivery)

	if emit != nil && s.reasoning != "" {
		if err := emit(StreamChunk{ID: request.ID, Model: request.Model, ReasoningDelta: s.reasoning}); err != nil {
			s.record(request.ID, target.User.ID, OutcomeCanceled, started)
			return Response{}, err
		}
	}

	reply, err := awaitReply(requestCtx, replies)
	if err != nil {
		if errors.Is(context.Cause(requestCtx), ErrServiceReloading) {
			s.record(request.ID, target.User.ID, OutcomeCanceled, started)
			return Response{}, ErrServiceReloading
		}
		outcome := OutcomeCanceled
		terminalErr := ErrRequestCanceled
		if errors.Is(err, context.DeadlineExceeded) {
			outcome = OutcomeTimedOut
			terminalErr = ErrRequestTimeout
		}
		s.record(request.ID, target.User.ID, outcome, started)
		return Response{}, fmt.Errorf("%w: %v", terminalErr, err)
	}

	response := Response{
		ID:        request.ID,
		Model:     request.Model,
		Reasoning: s.reasoning,
		Content:   reply.Content,
	}
	s.record(request.ID, target.User.ID, OutcomeResponded, started)

	if emit != nil {
		if err := emit(StreamChunk{ID: request.ID, Model: request.Model, ContentDelta: reply.Content}); err != nil {
			return Response{}, err
		}
		if err := emit(StreamChunk{ID: request.ID, Model: request.Model, Done: true}); err != nil {
			return Response{}, err
		}
	}

	return response, nil
}

func validateRequest(request Request) error {
	if strings.TrimSpace(request.Model) == "" {
		return fmt.Errorf("%w: model is required", ErrInvalidRequest)
	}
	if len(request.Messages) == 0 {
		return fmt.Errorf("%w: at least one message is required", ErrInvalidRequest)
	}
	if len(request.ID) > 128 {
		return fmt.Errorf("%w: request ID is too long", ErrInvalidRequest)
	}
	for i, message := range request.Messages {
		if strings.TrimSpace(message.Role) == "" {
			return fmt.Errorf("%w: message %d has no role", ErrInvalidRequest, i)
		}
	}
	return nil
}

func awaitReply(ctx context.Context, replies <-chan Reply) (Reply, error) {
	select {
	case reply := <-replies:
		return reply, nil
	case <-ctx.Done():
		// Prefer a reply that won the broker race just before cancellation.
		select {
		case reply := <-replies:
			return reply, nil
		default:
			return Reply{}, ctx.Err()
		}
	}
}

func (s *Service) record(requestID, userID string, outcome Outcome, started time.Time) {
	s.recorder.Record(Observation{
		RequestID: requestID,
		UserID:    userID,
		Outcome:   outcome,
		Latency:   time.Since(started),
	})
}

type discardRecorder struct{}

func (discardRecorder) Record(Observation) {}
