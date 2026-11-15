package app

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/redact"
	"marshal/internal/tools/native"
)

func TestWorkOutputCacheInvalidatesRegisteredSecrets(t *testing.T) {
	for _, boundary := range []bool{false, true} {
		var cache workOutputCache
		secret := fmt.Sprintf("opaque-work-cache-%d-%v", time.Now().UnixNano(), boundary)
		raw, want := "printed "+secret, "printed "+redact.MaskToken
		if boundary {
			suffix := strings.Repeat("x", workTailBytes-len(redact.MaskToken))
			raw, want = secret+suffix, redact.MaskToken+suffix
		}
		cache.clean("key", "r1", raw)
		redact.RegisterSecret(secret)
		if got := cache.clean("key", "r1", raw); got != want {
			t.Fatalf("cache did not re-clean full input after secret registration (boundary=%v)", boundary)
		}
	}
}

// Repeatedly projecting a completed command must not repeatedly allocate the
// sanitized output, including a retained job discarded by durable precedence.
func TestCompletedCommandProjectionReusesCleanedOutput(t *testing.T) {
	raw := strings.Repeat("Output: output row\x1b[0m\n", 6000)
	jobs := &native.JobSnapshot{Epoch: "epoch", Jobs: []native.JobInfo{{
		ExecutionID: "execution", SourceEpoch: "epoch", ResultRevision: "r1",
		Status: native.StatusCompleted, OutputTail: raw,
	}}}
	exits := []session.JobExit{{ExecutionID: "execution", SourceEpoch: "epoch", ResultRevision: "r1", State: "completed", Output: "durable prefix\n" + raw}}
	wc := &workCoordinator{}
	wc.commandCandidates("conversation", jobs, exits)
	allocs := testing.AllocsPerRun(3, func() {
		items := wc.commandCandidates("conversation", jobs, exits)
		if len(items) != 1 || !strings.HasSuffix(items[0].item.OutputTail, "output row\n") {
			t.Fatal("completed output changed")
		}
	})
	if allocs > 1000 {
		t.Fatalf("settled projection reprocessed output: %.0f allocations", allocs)
	}
}

func TestCompletedCommandProjectionKeepsFreshFields(t *testing.T) {
	wc := &workCoordinator{}
	jobs := &native.JobSnapshot{Epoch: "epoch", Jobs: []native.JobInfo{{
		ExecutionID: "execution", SourceEpoch: "epoch", ResultRevision: "r1",
		Status: native.StatusFailed, OutputTail: "first tail", DisplayTail: "first activity",
	}}}
	first := wc.commandCandidates("conversation", jobs, nil)[0].item
	if first.OutputTail != "first tail" {
		t.Fatal(first)
	}
	jobs.Jobs[0].OutputTail = "corrected tail"
	jobs.Jobs[0].DisplayTail = "fresh activity"
	jobs.SaveErrors = map[string]string{settlementKeyOf(jobs.Jobs[0]): "save failed"}
	got := wc.commandCandidates("conversation", jobs, nil)[0].item
	if got.OutputTail != "corrected tail" || got.Activity != "fresh activity" || got.Attention != "save failed" || got.Capabilities.Acknowledge {
		t.Fatalf("stale projection after same-revision update: %+v", got)
	}
	jobs.Jobs[0].Status = native.StatusRunning
	jobs.Jobs[0].OutputTail = "live tail"
	jobs.SaveErrors = nil
	got = wc.commandCandidates("conversation", jobs, nil)[0].item
	if got.State != "running" || got.OutputTail != "live tail" {
		t.Fatal(got)
	}
}

func TestWorkOutputCacheInvalidationAndBounds(t *testing.T) {
	var cache workOutputCache
	secret := "OPENAI_API_KEY=sk-proj-1234567890abcdefGHIJ"
	for range 2 {
		if got := cache.clean("secret", "r1", secret); strings.Contains(got, "sk-proj-") {
			t.Fatal("cached tail exposed a secret")
		}
	}
	if got := cache.clean("key", "r1", "\x1b[31mred\x1b[0m"); got != "red" {
		t.Fatal(got)
	}
	if got := cache.clean("key", "r1", "changed"); got != "changed" {
		t.Fatal("same revision returned stale output:", got)
	}
	if got := cache.clean("other epoch/key", "r1", "other"); got != "other" {
		t.Fatal(got)
	}
	for i := 0; i < 100; i++ {
		cache.clean(fmt.Sprint(i), "r1", strings.Repeat("x", 64<<10))
	}
	if cache.used > workOutputCacheBytes || cache.lru.Len() > workOutputCacheEntries {
		t.Fatalf("unbounded cache: %d bytes, %d entries", cache.used, cache.lru.Len())
	}
	cache.clean("oversized", "r1", strings.Repeat("x", workOutputCacheBytes+1))
	if _, ok := cache.entries["oversized"]; ok {
		t.Fatal("retained oversized input")
	}
	cache.clean("keep", "r1", "tail")
	cache.prune(map[string]int{"keep": 0})
	if len(cache.entries) != 1 || cache.entries["keep"] == nil {
		t.Fatal("did not prune dropped history")
	}
}

func TestCompletedCommandProjectionPrunesHistoryAndSeparatesEpochs(t *testing.T) {
	wc := &workCoordinator{}
	exits := []session.JobExit{
		{ExecutionID: "same", SourceEpoch: "old", State: "completed", Output: "old output"},
		{ExecutionID: "same", SourceEpoch: "new", State: "completed", Output: "new output"},
		{ID: "legacy", State: "completed", Output: "legacy output"},
	}
	items := wc.commandCandidates("conv", nil, exits)
	if len(items) != 3 || items[0].item.OutputTail != "old output" || items[1].item.OutputTail != "new output" || items[2].item.OutputTail != "legacy output" {
		t.Fatal(items)
	}
	wc.commandCandidates("conv", nil, nil)
	if wc.outputCache.used != 0 || len(wc.outputCache.entries) != 0 {
		t.Fatal("dropped history retained cached output")
	}
}

func TestWorkOutputCacheConcurrentCorrections(t *testing.T) {
	var cache workOutputCache
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			redact.RegisterSecret(fmt.Sprintf("concurrent-cache-registry-%d", i))
			input := fmt.Sprint(i)
			for range 100 {
				if got := cache.clean("same key", "r1", input); got != input {
					t.Errorf("got stale output %q for %q", got, input)
				}
				cache.prune(map[string]int{"same key": 0})
			}
		}(i)
	}
	wg.Wait()
}
