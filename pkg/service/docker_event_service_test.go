package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/client"
	"github.com/sparkfabrik/http-proxy/pkg/logger"
)

// fakeHandler is a no-op EventHandler for exercising the event loop.
type fakeHandler struct {
	scanErr error
}

func (f *fakeHandler) HandleInitialScan(ctx context.Context) error       { return f.scanErr }
func (f *fakeHandler) HandleEvent(context.Context, events.Message) error { return nil }
func (f *fakeHandler) GetName() string                                   { return "fake" }
func (f *fakeHandler) SetDependencies(*client.Client, *logger.Logger)    {}

func newTestService(h EventHandler, subscribe eventSubscriber) *Service {
	return &Service{
		logger:         logger.New("test"),
		handler:        h,
		serviceName:    "test",
		subscribe:      subscribe,
		reconnectDelay: time.Millisecond,
	}
}

func waitSignal(t *testing.T, ch <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal(msg)
	}
}

func TestRunEventLoopReconnectsOnClosedEventsChannel(t *testing.T) {
	calls := make(chan struct{}, 10)
	var mu sync.Mutex
	var current chan events.Message

	subscribe := func(context.Context, events.ListOptions) (<-chan events.Message, <-chan error) {
		ev := make(chan events.Message)
		mu.Lock()
		current = ev
		mu.Unlock()
		calls <- struct{}{}
		return ev, make(chan error)
	}

	s := newTestService(&fakeHandler{}, subscribe)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.runEventLoop(ctx) }()

	waitSignal(t, calls, "event loop did not make the initial subscription")

	// Simulate the Docker daemon closing the stream.
	mu.Lock()
	close(current)
	mu.Unlock()

	waitSignal(t, calls, "event loop did not reconnect after the stream closed")

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runEventLoop returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event loop did not stop after context cancellation")
	}
}

func TestRunEventLoopReconnectsOnError(t *testing.T) {
	calls := make(chan struct{}, 10)
	var mu sync.Mutex
	var currentErr chan error

	subscribe := func(context.Context, events.ListOptions) (<-chan events.Message, <-chan error) {
		er := make(chan error, 1)
		mu.Lock()
		currentErr = er
		mu.Unlock()
		calls <- struct{}{}
		return make(chan events.Message), er
	}

	s := newTestService(&fakeHandler{}, subscribe)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.runEventLoop(ctx) }()

	waitSignal(t, calls, "event loop did not make the initial subscription")

	// Deliver a stream error, which should trigger a reconnect.
	mu.Lock()
	currentErr <- errors.New("boom")
	mu.Unlock()

	waitSignal(t, calls, "event loop did not reconnect after a stream error")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("event loop did not stop after context cancellation")
	}
}

func TestRunEventLoopReturnsInitialScanError(t *testing.T) {
	wantErr := errors.New("scan failed")
	subscribe := func(context.Context, events.ListOptions) (<-chan events.Message, <-chan error) {
		t.Error("subscribe should not be called when the initial scan fails")
		return nil, nil
	}

	s := newTestService(&fakeHandler{scanErr: wantErr}, subscribe)
	if err := s.runEventLoop(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("runEventLoop error = %v, want %v", err, wantErr)
	}
}
