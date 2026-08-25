package completion

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Xbai-hang/sotapi/internal/routing"
)

func TestServiceCompleteLifecycle(t *testing.T) {
	service, deliverer, recorder := newTestService(t, nil)
	request := Request{
		ID:       "request-1",
		Model:    "human",
		Messages: []Message{{Role: "user", Content: "What do you think?"}},
	}

	resultChannel := make(chan completionResult, 1)
	go func() {
		response, err := service.Complete(context.Background(), request)
		resultChannel <- completionResult{response: response, err: err}
	}()

	delivered := receive(t, deliverer.deliveries)
	if delivered.target.User.ID != "alice" || delivered.task.RequestID != request.ID || delivered.task.Model != request.Model {
		t.Fatalf("delivered task = %#v", delivered)
	}
	if err := service.SubmitReply(request.ID, "A considered human answer."); err != nil {
		t.Fatalf("SubmitReply() error = %v", err)
	}

	result := receive(t, resultChannel)
	if result.err != nil {
		t.Fatalf("Complete() error = %v", result.err)
	}
	if result.response.ID != request.ID || result.response.Model != request.Model || result.response.Content != "A considered human answer." {
		t.Fatalf("Complete() response = %#v", result.response)
	}
	if result.response.Reasoning != "A human is thinking." {
		t.Fatalf("reasoning = %q", result.response.Reasoning)
	}
	if forgotten := receive(t, deliverer.forgotten); forgotten.ID != "delivery-request-1" {
		t.Fatalf("forgotten delivery = %#v", forgotten)
	}
	observation := receive(t, recorder.observations)
	if observation.Outcome != OutcomeResponded || observation.UserID != "alice" || observation.RequestID != request.ID {
		t.Fatalf("observation = %#v", observation)
	}
	if count := pendingCount(service.pending); count != 0 {
		t.Fatalf("pending entries = %d, want 0", count)
	}
}

func TestServiceStreamEmitsReasoningBeforeReply(t *testing.T) {
	service, deliverer, recorder := newTestService(t, nil)
	events := make(chan StreamChunk, 3)
	result := make(chan error, 1)
	go func() {
		result <- service.Stream(context.Background(), validRequest("stream-1"), func(chunk StreamChunk) error {
			events <- chunk
			return nil
		})
	}()

	receive(t, deliverer.deliveries)
	reasoning := receive(t, events)
	if reasoning.ReasoningDelta != "A human is thinking." || reasoning.ContentDelta != "" || reasoning.Done {
		t.Fatalf("first stream chunk = %#v", reasoning)
	}
	if err := service.SubmitReply("stream-1", "streamed answer"); err != nil {
		t.Fatalf("SubmitReply() error = %v", err)
	}
	content := receive(t, events)
	done := receive(t, events)
	if content.ContentDelta != "streamed answer" || content.ID != "stream-1" || content.Model != "human" {
		t.Fatalf("content chunk = %#v", content)
	}
	if !done.Done || done.ContentDelta != "" || done.ReasoningDelta != "" {
		t.Fatalf("done chunk = %#v", done)
	}
	if err := receive(t, result); err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	receive(t, deliverer.forgotten)
	if observation := receive(t, recorder.observations); observation.Outcome != OutcomeResponded {
		t.Fatalf("observation = %#v", observation)
	}
}

func TestServiceTimeoutCleansUpAndRejectsLateReply(t *testing.T) {
	service, deliverer, recorder := newTestService(t, nil)
	request := validRequest("timeout-1")
	request.Timeout = 20 * time.Millisecond

	_, err := service.Complete(context.Background(), request)
	if !errors.Is(err, ErrRequestTimeout) {
		t.Fatalf("Complete() error = %v, want ErrRequestTimeout", err)
	}
	receive(t, deliverer.deliveries)
	receive(t, deliverer.forgotten)
	if observation := receive(t, recorder.observations); observation.Outcome != OutcomeTimedOut {
		t.Fatalf("observation = %#v", observation)
	}
	if err := service.SubmitReply(request.ID, "too late"); !errors.Is(err, ErrUnknownRequest) {
		t.Fatalf("late SubmitReply() error = %v, want ErrUnknownRequest", err)
	}
	if count := pendingCount(service.pending); count != 0 {
		t.Fatalf("pending entries = %d, want 0", count)
	}
}

func TestServiceCancellation(t *testing.T) {
	service, deliverer, recorder := newTestService(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := service.Complete(ctx, validRequest("cancel-1"))
		result <- err
	}()
	receive(t, deliverer.deliveries)
	cancel()

	if err := receive(t, result); !errors.Is(err, ErrRequestCanceled) {
		t.Fatalf("Complete() error = %v, want ErrRequestCanceled", err)
	}
	receive(t, deliverer.forgotten)
	if observation := receive(t, recorder.observations); observation.Outcome != OutcomeCanceled {
		t.Fatalf("observation = %#v", observation)
	}
}

func TestServiceReloadCancellationCleansUpPendingRequest(t *testing.T) {
	service, deliverer, recorder := newTestService(t, nil)
	ctx, cancel := context.WithCancelCause(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := service.Complete(ctx, validRequest("reload-1"))
		result <- err
	}()
	receive(t, deliverer.deliveries)
	cancel(ErrServiceReloading)

	if err := receive(t, result); !errors.Is(err, ErrServiceReloading) {
		t.Fatalf("Complete() error = %v, want ErrServiceReloading", err)
	}
	receive(t, deliverer.forgotten)
	if observation := receive(t, recorder.observations); observation.Outcome != OutcomeCanceled {
		t.Fatalf("observation = %#v", observation)
	}
	if err := service.SubmitReply("reload-1", "too late"); !errors.Is(err, ErrUnknownRequest) {
		t.Fatalf("late SubmitReply() error = %v, want ErrUnknownRequest", err)
	}
	if count := pendingCount(service.pending); count != 0 {
		t.Fatalf("pending entries = %d, want 0", count)
	}
}

func TestServiceDeliveryFailure(t *testing.T) {
	deliveryError := errors.New("network down")
	service, deliverer, recorder := newTestService(t, deliveryError)
	_, err := service.Complete(context.Background(), validRequest("delivery-1"))
	if !errors.Is(err, ErrDeliveryFailed) || !strings.Contains(err.Error(), deliveryError.Error()) {
		t.Fatalf("Complete() error = %v", err)
	}
	receive(t, deliverer.deliveries)
	select {
	case forgotten := <-deliverer.forgotten:
		t.Fatalf("unexpected forgotten delivery %#v", forgotten)
	default:
	}
	if observation := receive(t, recorder.observations); observation.Outcome != OutcomeDeliveryFailed {
		t.Fatalf("observation = %#v", observation)
	}
}

func TestServiceStreamEmitterFailureCancelsPendingRequest(t *testing.T) {
	service, deliverer, recorder := newTestService(t, nil)
	emitterError := errors.New("client write failed")
	err := service.Stream(context.Background(), validRequest("write-1"), func(StreamChunk) error {
		return emitterError
	})
	if !errors.Is(err, emitterError) {
		t.Fatalf("Stream() error = %v", err)
	}
	receive(t, deliverer.deliveries)
	receive(t, deliverer.forgotten)
	if observation := receive(t, recorder.observations); observation.Outcome != OutcomeCanceled {
		t.Fatalf("observation = %#v", observation)
	}
	if err := service.SubmitReply("write-1", "too late"); !errors.Is(err, ErrUnknownRequest) {
		t.Fatalf("SubmitReply() error = %v, want ErrUnknownRequest", err)
	}
}

func TestServiceGeneratesRequestID(t *testing.T) {
	service, deliverer, _ := newTestService(t, nil)
	result := make(chan completionResult, 1)
	request := validRequest("")
	go func() {
		response, err := service.Complete(context.Background(), request)
		result <- completionResult{response: response, err: err}
	}()
	delivered := receive(t, deliverer.deliveries)
	if !strings.HasPrefix(delivered.task.RequestID, "chatcmpl-") {
		t.Fatalf("generated request ID = %q", delivered.task.RequestID)
	}
	if err := service.SubmitReply(delivered.task.RequestID, "answer"); err != nil {
		t.Fatalf("SubmitReply() error = %v", err)
	}
	completed := receive(t, result)
	if completed.err != nil || completed.response.ID != delivered.task.RequestID {
		t.Fatalf("Complete() = %#v, %v", completed.response, completed.err)
	}
	receive(t, deliverer.forgotten)
}

func TestServiceRejectsDuplicateReply(t *testing.T) {
	service, deliverer, _ := newTestService(t, nil)
	result := make(chan error, 1)
	go func() {
		_, err := service.Complete(context.Background(), validRequest("duplicate-1"))
		result <- err
	}()
	receive(t, deliverer.deliveries)
	if err := service.SubmitReply("duplicate-1", "first"); err != nil {
		t.Fatalf("first SubmitReply() error = %v", err)
	}
	if err := service.SubmitReply("duplicate-1", "second"); !errors.Is(err, ErrUnknownRequest) {
		t.Fatalf("second SubmitReply() error = %v, want ErrUnknownRequest", err)
	}
	if err := receive(t, result); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	receive(t, deliverer.forgotten)
}

func TestServiceValidation(t *testing.T) {
	router := testRouter(t)
	deliverer := newFakeDeliverer(nil)
	if _, err := NewService(nil, deliverer, nil, ServiceConfig{RequestTimeout: time.Second}); err == nil {
		t.Fatal("NewService() with nil router succeeded")
	}
	if _, err := NewService(router, nil, nil, ServiceConfig{RequestTimeout: time.Second}); err == nil {
		t.Fatal("NewService() with nil deliverer succeeded")
	}
	if _, err := NewService(router, deliverer, nil, ServiceConfig{}); err == nil {
		t.Fatal("NewService() with zero timeout succeeded")
	}
	service, err := NewService(router, deliverer, nil, ServiceConfig{RequestTimeout: time.Second})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	requests := []Request{
		{Messages: []Message{{Role: "user"}}},
		{Model: "human"},
		{Model: "human", Messages: []Message{{Content: "missing role"}}},
		{ID: strings.Repeat("x", 129), Model: "human", Messages: []Message{{Role: "user"}}},
	}
	for _, request := range requests {
		if _, err := service.Complete(context.Background(), request); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("Complete(%#v) error = %v, want ErrInvalidRequest", request, err)
		}
	}
	if err := service.Stream(context.Background(), validRequest("stream"), nil); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Stream(nil emitter) error = %v", err)
	}
	if err := service.SubmitReply("", "answer"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("SubmitReply(empty ID) error = %v", err)
	}
	if err := service.SubmitReply("request", "   "); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("SubmitReply(empty content) error = %v", err)
	}
	if _, err := service.Complete(context.Background(), Request{Model: "missing", Messages: []Message{{Role: "user"}}}); !errors.Is(err, routing.ErrModelNotFound) {
		t.Fatalf("Complete(unknown model) error = %v", err)
	}
}

func TestPendingBrokerConsumesReplyExactlyOnce(t *testing.T) {
	broker := newPendingBroker()
	replies, unregister, err := broker.register("request")
	if err != nil {
		t.Fatalf("register() error = %v", err)
	}
	if _, _, err := broker.register("request"); !errors.Is(err, errDuplicateRequest) {
		t.Fatalf("duplicate register() error = %v", err)
	}
	if !broker.resolve(Reply{RequestID: "request", Content: "first"}) {
		t.Fatal("first resolve() = false")
	}
	if broker.resolve(Reply{RequestID: "request", Content: "second"}) {
		t.Fatal("second resolve() = true")
	}
	if reply := receive(t, replies); reply.Content != "first" {
		t.Fatalf("reply = %#v", reply)
	}
	unregister()
	if count := pendingCount(broker); count != 0 {
		t.Fatalf("pending entries = %d, want 0", count)
	}
}

func TestAwaitReplyPrefersAlreadyResolvedReplyOverCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	replies := make(chan Reply, 1)
	replies <- Reply{RequestID: "request", Content: "answer"}
	cancel()
	reply, err := awaitReply(ctx, replies)
	if err != nil || reply.Content != "answer" {
		t.Fatalf("awaitReply() = %#v, %v", reply, err)
	}
}

type deliveredTask struct {
	target routing.Target
	task   Task
}

type fakeDeliverer struct {
	deliveries chan deliveredTask
	forgotten  chan Delivery
	err        error
}

func newFakeDeliverer(err error) *fakeDeliverer {
	return &fakeDeliverer{
		deliveries: make(chan deliveredTask, 8),
		forgotten:  make(chan Delivery, 8),
		err:        err,
	}
}

func (d *fakeDeliverer) Deliver(_ context.Context, target routing.Target, task Task) (Delivery, error) {
	d.deliveries <- deliveredTask{target: target, task: task}
	if d.err != nil {
		return Delivery{}, d.err
	}
	return Delivery{ID: "delivery-" + task.RequestID}, nil
}

func (d *fakeDeliverer) Forget(delivery Delivery) {
	d.forgotten <- delivery
}

type fakeRecorder struct {
	observations chan Observation
}

func (r *fakeRecorder) Record(observation Observation) {
	r.observations <- observation
}

type completionResult struct {
	response Response
	err      error
}

func newTestService(t *testing.T, deliveryError error) (*Service, *fakeDeliverer, *fakeRecorder) {
	t.Helper()
	deliverer := newFakeDeliverer(deliveryError)
	recorder := &fakeRecorder{observations: make(chan Observation, 8)}
	service, err := NewService(testRouter(t), deliverer, recorder, ServiceConfig{
		RequestTimeout:    time.Second,
		ReasoningTemplate: "A human is thinking.",
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service, deliverer, recorder
}

func testRouter(t *testing.T) *routing.Router {
	t.Helper()
	router, err := routing.NewRouter(
		[]routing.Model{{ID: "human", PoolID: "pool"}},
		[]routing.Pool{{ID: "pool", UserIDs: []string{"alice"}}},
		[]routing.User{{ID: "alice", Channel: "telegram", Recipient: "123"}},
	)
	if err != nil {
		t.Fatalf("routing.NewRouter() error = %v", err)
	}
	return router
}

func validRequest(id string) Request {
	return Request{ID: id, Model: "human", Messages: []Message{{Role: "user", Content: "hello"}}}
}

func receive[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel value")
		var zero T
		return zero
	}
}

func pendingCount(broker *pendingBroker) int {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	return len(broker.entries)
}
