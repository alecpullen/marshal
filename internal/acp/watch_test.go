package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/pubsub"
	"marshal/internal/watch"
)

type watchNotices struct {
	mu   sync.Mutex
	list []SessionUpdateParams
}

func (n *watchNotices) notify(method string, params any) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if p, ok := params.(SessionUpdateParams); ok && method == "session/update" {
		n.list = append(n.list, p)
	}
	return nil
}

func (n *watchNotices) kinds(kind string) []SessionUpdateParams {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []SessionUpdateParams
	for _, p := range n.list {
		if p.Update["kind"] == kind {
			out = append(out, p)
		}
	}
	return out
}

func newWatchTestACP(t *testing.T) (*WatchManagerACP, *watchNotices) {
	t.Helper()
	broker := pubsub.NewBroker[watch.Event]()
	wm := watch.NewManager(context.Background(), watch.Deps{
		OnEvent: func(ev watch.Event) { broker.Publish("watch", ev) },
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = wm.Close(ctx)
		broker.Close()
	})
	notices := &watchNotices{}
	acpWatch := NewWatchManagerACP(func(id string) (*watch.Manager, *pubsub.Broker[watch.Event], bool) {
		return wm, broker, id == "s1"
	}, notices.notify)
	return acpWatch, notices
}

func TestWatchStartListStopAndUpdates(t *testing.T) {
	m, notices := newWatchTestACP(t)
	target := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec, _ := json.Marshal(map[string]any{
		"sessionId": "s1",
		"spec": map[string]any{
			"name": "file-watch", "kind": "file", "path": target,
			"condition": "exit_code 0", "mode": "repeat", "intervalMs": 100,
		},
	})
	v, err := m.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	res := v.(map[string]any)
	id := res["id"].(string)
	if id == "" || res["name"] != "file-watch" {
		t.Fatalf("start result = %v", res)
	}

	listed, err := m.List(context.Background(), json.RawMessage(`{"sessionId":"s1"}`))
	if err != nil {
		t.Fatal(err)
	}
	ws := listed.(map[string]any)["watches"].([]WatchInfoWire)
	if len(ws) != 1 || ws[0].ID != id || ws[0].Owner != "studio" || ws[0].Kind != "file" || ws[0].Samples == nil {
		t.Fatalf("watches = %+v", ws)
	}

	waitForWithin(t, 8*time.Second, "a watch update for the fired sample", func() bool {
		for _, p := range notices.kinds("watch") {
			ev := p.Update["event"].(map[string]any)
			if ev["watchId"] == id && ev["state"] == "fired" && ev["owner"] == "studio" {
				return true
			}
		}
		return false
	})
	waitForWithin(t, 8*time.Second, "samples in the list", func() bool {
		l, _ := m.List(context.Background(), json.RawMessage(`{"sessionId":"s1"}`))
		got := l.(map[string]any)["watches"].([]WatchInfoWire)
		return len(got) == 1 && len(got[0].Samples) > 0 && got[0].Samples[0].Tripped
	})

	if _, err := m.Stop(context.Background(), json.RawMessage(`{"sessionId":"s1","id":"`+id+`"}`)); err != nil {
		t.Fatal(err)
	}
	listed, _ = m.List(context.Background(), json.RawMessage(`{"sessionId":"s1"}`))
	if got := listed.(map[string]any)["watches"].([]WatchInfoWire); len(got) != 0 {
		t.Fatalf("watches after stop = %+v", got)
	}
}

func TestWatchStartOverLimitErrors(t *testing.T) {
	m, _ := newWatchTestACP(t)
	target := filepath.Join(t.TempDir(), "f.txt")
	_ = os.WriteFile(target, []byte("x"), 0o644)
	var lastErr error
	for i := 0; i <= watch.MaxWatches; i++ {
		spec, _ := json.Marshal(map[string]any{
			"sessionId": "s1",
			"spec":      map[string]any{"name": "w", "kind": "file", "path": target, "intervalMs": 3600000},
		})
		_, lastErr = m.Start(context.Background(), spec)
	}
	if lastErr == nil || !strings.Contains(lastErr.Error(), "watch cap reached") {
		t.Fatalf("err = %v", lastErr)
	}
	rpc, ok := lastErr.(*jsonRPCError)
	if !ok || rpc.Code != invalidParams {
		t.Fatalf("err = %#v, want invalid params", lastErr)
	}
}

func TestWatchUnknownSession(t *testing.T) {
	m, _ := newWatchTestACP(t)
	if _, err := m.List(context.Background(), json.RawMessage(`{"sessionId":"nope"}`)); err == nil {
		t.Fatal("want error")
	}
	if _, err := m.Stop(context.Background(), json.RawMessage(`{"sessionId":"s1"}`)); err == nil {
		t.Fatal("want error for missing id")
	}
}

// waitForWithin is waitFor with a longer deadline: watches poll no faster
// than watch.MinInterval.
func waitForWithin(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
