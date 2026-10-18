package session

import (
	"context"
	"sync"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/pubsub"
)

func TestEffortOverrideRoundTrip(t *testing.T) {
	s := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	if got := s.EffortOverride(); got != (EffortOverride{}) {
		t.Fatalf("zero override = %+v", got)
	}
	want := EffortOverride{Level: "high", Provider: "anthropic", Model: "claude"}
	s.SetEffortOverride(want)
	if got := s.EffortOverride(); got != want {
		t.Fatalf("override = %+v, want %+v", got, want)
	}
}

func TestTurnRouteSetAndClear(t *testing.T) {
	s := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	if _, ok := s.TurnRoute(); ok {
		t.Fatal("no turn should be running initially")
	}
	want := RouteInfo{Provider: "ollama", Model: "qwen", Thinking: "medium", Active: true}
	s.SetTurnRoute(want)
	got, ok := s.TurnRoute()
	if !ok {
		t.Fatal("route should be set after SetTurnRoute")
	}
	if got != want {
		t.Fatalf("route = %+v, want %+v", got, want)
	}
	s.ClearTurnRoute()
	if _, ok := s.TurnRoute(); ok {
		t.Fatal("route should be cleared between turns")
	}
}

func TestEffortStateConcurrent(t *testing.T) {
	s := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.SetEffortOverride(EffortOverride{Level: "low", Provider: "p", Model: "m"})
			_ = s.EffortOverride()
			s.SetTurnRoute(RouteInfo{Provider: "p", Model: "m"})
			_, _ = s.TurnRoute()
			s.ClearTurnRoute()
		}()
	}
	wg.Wait()
}

func TestPublishEffortChange(t *testing.T) {
	s := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	broker := pubsub.NewBroker[Event]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := broker.Subscribe(ctx)
	s.SetEventBroker(broker)
	s.PublishEffortChange(EffortChange{Effective: "high", Pending: "low", AppliesFrom: EffortAppliesNextTurn})
	select {
	case ev := <-ch:
		if ev.Type != EventEffortChanged || ev.Payload.Effort == nil {
			t.Fatalf("event = %+v", ev)
		}
		if ev.Payload.Effort.Effective != "high" || ev.Payload.Effort.Pending != "low" || ev.Payload.Effort.AppliesFrom != EffortAppliesNextTurn {
			t.Fatalf("payload = %+v", ev.Payload.Effort)
		}
	case <-time.After(time.Second):
		t.Fatal("no event for an effort change")
	}
}
