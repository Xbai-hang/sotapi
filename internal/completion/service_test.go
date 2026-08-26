package completion

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Xbai-hang/sotapi/internal/availability"
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

	response, err := service.Complete(context.Background(), request)
	if err != nil || response.Content != "fallback answer" || response.Reasoning != "A human is thinking." {
		t.Fatalf("Complete() = %#v, %v", response, err)
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

func TestServiceThirdMissedReplyTakesUserOfflineAndNotifiesOnce(t *testing.T) {
	service, deliverer, _ := newTestService(t, nil)
	for attempt := 1; attempt <= 3; attempt++ {
		request := validRequest(fmt.Sprintf("timeout-%d", attempt))
		request.Timeout = 10 * time.Millisecond
		response, err := service.Complete(context.Background(), request)
		if err != nil || response.Content != "fallback answer" {
			t.Fatalf("attempt %d Complete() = %#v, %v", attempt, response, err)
		}
		receive(t, deliverer.deliveries)
		receive(t, deliverer.forgotten)
	}

	notification := receive(t, deliverer.notifications)
	if notification.target.User.ID != "alice" || notification.notification.Kind != NotificationAutoOffline || notification.notification.MissedReplies != 3 {
		t.Fatalf("notification = %#v", notification)
	}
	select {
	case duplicate := <-deliverer.notifications:
		t.Fatalf("duplicate notification = %#v", duplicate)
	default:
	}
	status, _ := service.availability.Status("alice")
	if status.Online || status.MissedReplies != 3 {
		t.Fatalf("availability status = %#v", status)
	}
}

func TestServiceNoOnlineUserReturnsImmediateFallbackWithoutDelivery(t *testing.T) {
	service, deliverer, _ := newTestService(t, nil)
	for attempt := 1; attempt <= 3; attempt++ {
		request := validRequest(fmt.Sprintf("offline-%d", attempt))
		request.Timeout = time.Millisecond
		if _, err := service.Complete(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		receive(t, deliverer.deliveries)
		receive(t, deliverer.forgotten)
	}
	receive(t, deliverer.notifications)

	started := time.Now()
	response, err := service.Complete(context.Background(), validRequest("fallback-now"))
	if err != nil || response.Content != "fallback answer" || response.Reasoning != "" {
		t.Fatalf("Complete() = %#v, %v", response, err)
	}
	if time.Since(started) > 100*time.Millisecond {
		t.Fatal("fallback for offline user was not immediate")
	}
	select {
	case delivery := <-deliverer.deliveries:
		t.Fatalf("offline user received delivery %#v", delivery)
	default:
	}
}

func TestServiceReplyAndOnlineCommandResetMissedReplies(t *testing.T) {
	service, deliverer, _ := newTestService(t, nil)
	request := validRequest("miss-once")
	request.Timeout = time.Millisecond
	if _, err := service.Complete(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	receive(t, deliverer.deliveries)
	receive(t, deliverer.forgotten)

	result := make(chan error, 1)
	go func() {
		_, err := service.Complete(context.Background(), validRequest("reply-reset"))
		result <- err
	}()
	receive(t, deliverer.deliveries)
	if err := service.SubmitReply("reply-reset", "answer"); err != nil {
		t.Fatal(err)
	}
	if err := receive(t, result); err != nil {
		t.Fatal(err)
	}
	receive(t, deliverer.forgotten)
	status, _ := service.availability.Status("alice")
	if status.MissedReplies != 0 || !status.Online {
		t.Fatalf("status after reply = %#v", status)
	}

	for attempt := 0; attempt < 3; attempt++ {
		timedOut := validRequest(fmt.Sprintf("online-%d", attempt))
		timedOut.Timeout = time.Millisecond
		if _, err := service.Complete(context.Background(), timedOut); err != nil {
			t.Fatal(err)
		}
		receive(t, deliverer.deliveries)
		receive(t, deliverer.forgotten)
	}
	if _, err := service.SetOnline("telegram", "123"); err != nil {
		t.Fatalf("SetOnline() error = %v", err)
	}
	status, _ = service.availability.Status("alice")
	if !status.Online || status.MissedReplies != 0 {
		t.Fatalf("status after /online = %#v", status)
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

func TestServiceCallerDeadlineDoesNotCountAsMissedReply(t *testing.T) {
	service, deliverer, recorder := newTestService(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	request := validRequest("caller-deadline")
	request.Timeout = time.Second

	_, err := service.Complete(ctx, request)
	if !errors.Is(err, ErrRequestCanceled) {
		t.Fatalf("Complete() error = %v, want ErrRequestCanceled", err)
	}
	receive(t, deliverer.deliveries)
	receive(t, deliverer.forgotten)
	if observation := receive(t, recorder.observations); observation.Outcome != OutcomeCanceled {
		t.Fatalf("observation = %#v", observation)
	}
	status, _ := service.availability.Status("alice")
	if !status.Online || status.MissedReplies != 0 {
		t.Fatalf("availability status = %#v", status)
	}
}

func TestServiceDisabledAutoOfflineContinuesRoutingAfterTimeouts(t *testing.T) {
	deliverer := newFakeDeliverer(nil)
	recorder := &fakeRecorder{observations: make(chan Observation, 8)}
	fallback, err := NewTemplateFallback("fallback answer")
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(testRouter(t), deliverer, recorder, testAvailability(t, false), fallback, ServiceConfig{RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 4; attempt++ {
		request := validRequest(fmt.Sprintf("disabled-%d", attempt))
		request.Timeout = time.Millisecond
		if _, err := service.Complete(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		receive(t, deliverer.deliveries)
		receive(t, deliverer.forgotten)
	}
	status, _ := service.availability.Status("alice")
	if !status.Online || status.MissedReplies != 0 {
		t.Fatalf("availability status = %#v", status)
	}
	select {
	case notification := <-deliverer.notifications:
		t.Fatalf("unexpected notification = %#v", notification)
	default:
	}
}

func TestServiceStreamImmediatelyEmitsFallbackForOfflineUser(t *testing.T) {
	service, deliverer, _ := newTestService(t, nil)
	for range 3 {
		if _, err := service.availability.RecordMissedReply("alice"); err != nil {
			t.Fatal(err)
		}
	}
	var chunks []StreamChunk
	err := service.Stream(context.Background(), validRequest("offline-stream"), func(chunk StreamChunk) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if len(chunks) != 2 || chunks[0].ContentDelta != "fallback answer" || chunks[0].ReasoningDelta != "" || !chunks[1].Done {
		t.Fatalf("fallback chunks = %#v", chunks)
	}
	select {
	case delivery := <-deliverer.deliveries:
		t.Fatalf("offline user received delivery %#v", delivery)
	default:
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
	response, err := service.Complete(context.Background(), validRequest("delivery-1"))
	if err != nil || response.Content != "fallback answer" {
		t.Fatalf("Complete() = %#v, %v", response, err)
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
	status, ok := service.availability.Status("alice")
	if !ok {
		t.Fatal("availability status for alice not found")
	}
	if !status.Online || status.MissedReplies != 0 {
		t.Fatalf("availability after delivery failure = %#v", status)
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
	state := testAvailability(t, true)
	fallback, err := NewTemplateFallback("fallback")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(nil, deliverer, nil, state, fallback, ServiceConfig{RequestTimeout: time.Second}); err == nil {
		t.Fatal("NewService() with nil router succeeded")
	}
	if _, err := NewService(router, nil, nil, state, fallback, ServiceConfig{RequestTimeout: time.Second}); err == nil {
		t.Fatal("NewService() with nil deliverer succeeded")
	}
	if _, err := NewService(router, deliverer, nil, nil, fallback, ServiceConfig{RequestTimeout: time.Second}); err == nil {
		t.Fatal("NewService() with nil availability succeeded")
	}
	if _, err := NewService(router, deliverer, nil, state, nil, ServiceConfig{RequestTimeout: time.Second}); err == nil {
		t.Fatal("NewService() with nil fallback succeeded")
	}
	if _, err := NewService(router, deliverer, nil, state, fallback, ServiceConfig{}); err == nil {
		t.Fatal("NewService() with zero timeout succeeded")
	}
	service, err := NewService(router, deliverer, nil, state, fallback, ServiceConfig{RequestTimeout: time.Second})
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
	if _, err := service.SetOnline("", "123"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("SetOnline(empty channel) error = %v, want ErrInvalidRequest", err)
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
	deliveries    chan deliveredTask
	forgotten     chan Delivery
	notifications chan deliveredNotification
	err           error
}

type deliveredNotification struct {
	target       routing.Target
	notification Notification
}

func newFakeDeliverer(err error) *fakeDeliverer {
	return &fakeDeliverer{
		deliveries:    make(chan deliveredTask, 8),
		forgotten:     make(chan Delivery, 8),
		notifications: make(chan deliveredNotification, 8),
		err:           err,
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

func (d *fakeDeliverer) Notify(_ context.Context, target routing.Target, notification Notification) {
	d.notifications <- deliveredNotification{target: target, notification: notification}
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
	fallback, err := NewTemplateFallback("fallback answer")
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(testRouter(t), deliverer, recorder, testAvailability(t, true), fallback, ServiceConfig{
		RequestTimeout:    time.Second,
		ReasoningTemplate: "A human is thinking.",
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service, deliverer, recorder
}

func testAvailability(t *testing.T, enabled bool) *availability.Store {
	t.Helper()
	store, err := availability.NewStore(
		[]routing.User{{ID: "alice", Channel: "telegram", Recipient: "123"}},
		availability.Config{Enabled: enabled, AfterMissedReplies: 3},
	)
	if err != nil {
		t.Fatalf("availability.NewStore() error = %v", err)
	}
	return store
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
