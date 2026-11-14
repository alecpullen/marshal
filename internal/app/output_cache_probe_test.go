//go:build latencyprobe

package app

// Investigation-only comparisons. These do not install a production cache or
// change coordinator scheduling, publication, redaction or durability.
import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/native"
)

type probeTailKey struct{ conversation, epoch, execution, revision string }
type probeTailEntry struct{ raw, clean string }
type probeTailMemo map[probeTailKey]probeTailEntry

// Exact input equality is deliberate: ResultRevision describes settlement,
// not a content hash, and AddJobExit can replace an existing record's text.
// This two-entry benchmark does not model production eviction or concurrency.
func (m probeTailMemo) clean(key probeTailKey, raw string) string {
	if e, ok := m[key]; ok && e.raw == raw {
		return e.clean
	}
	out := cleanWorkTail(raw)
	m[key] = probeTailEntry{raw, out}
	return out
}

func TestSettledTailMemoProbePreservesCleaning(t *testing.T) {
	m := probeTailMemo{}
	key := probeTailKey{"conversation", "epoch", "execution", "revision"}
	raw := "prefix\nOPENAI_API_KEY=sk-proj-1234567890abcdefGHIJ\nlast line"
	for _, input := range []string{raw, raw, raw + " changed", strings.Repeat("prefix\n", 2000) + raw} {
		got := m.clean(key, input)
		if got != cleanWorkTail(input) {
			t.Fatal("memo changed cleaning semantics")
		}
		if strings.Contains(got, "sk-proj-1234567890abcdef") {
			t.Fatal("memo exposed a secret")
		}
	}
	key.epoch = "new epoch"
	if got := m.clean(key, "different execution lifetime"); got != "different execution lifetime" {
		t.Fatal(got)
	}
}

func completedOutputFixture(b *testing.B) (*workCoordinator, *native.JobSnapshot, []session.JobExit, string) {
	b.Helper()
	path := os.Getenv("MARSHAL_LATENCY_OUTPUT_FILE")
	if path == "" {
		b.Skip("set MARSHAL_LATENCY_OUTPUT_FILE to captured real go-test JSON output")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}
	if len(data) < 1024 || !strings.Contains(string(data), `"Action":"pass"`) {
		b.Fatal("fixture must be nonempty real go-test JSON output")
	}
	out := string(data)
	zero := 0
	started := time.Unix(100, 0)
	ended := started.Add(time.Second)
	jobs := &native.JobSnapshot{Epoch: "probe-epoch"}
	state := session.New(config.Default(), b.TempDir(), started, session.Persistence{})
	var exits []session.JobExit
	for _, id := range []string{"test-1", "test-2"} {
		jobs.Jobs = append(jobs.Jobs, native.JobInfo{ID: id, ExecutionID: id, SourceEpoch: jobs.Epoch, ResultRevision: "result-" + id, Command: "go test -json ./internal/tools/registry -count=1", Status: native.StatusCompleted, Outcome: native.OutcomeCompleted, Attachment: native.AttachmentForeground, StartedAt: started, EndedAt: ended, ExitCode: &zero, OutputTail: out})
		exits = append(exits, session.JobExit{ID: id, ExecutionID: id, SourceEpoch: jobs.Epoch, ResultRevision: "result-" + id, Command: jobs.Jobs[len(jobs.Jobs)-1].Command, State: "completed", Outcome: "completed", Attachment: "foreground", StartedAt: started, EndedAt: ended, At: ended, Exit: &zero, Output: out})
		if err := state.AddJobExit(exits[len(exits)-1]); err != nil {
			b.Fatal(err)
		}
	}
	// Durable session exits have their own production 16 KiB cap; retained
	// manager jobs can still hold the full command output.
	exits = state.JobExitSnapshot()
	wc := &workCoordinator{}
	// For this all-settled fixture the existing precedence rule chooses durable
	// exits. Establish equivalence before measuring a projection that avoids
	// constructing the shadowed retained job items.
	full := wc.commandCandidates("probe", jobs, exits)
	durable := wc.commandCandidates("probe", nil, exits)
	if len(full) != 2 || !reflect.DeepEqual(full, durable) {
		b.Fatal("fixture does not exercise durable-over-retained precedence")
	}
	return wc, jobs, exits, out
}

var completedProjectionSink []workCandidate
var completedTailSink string

func BenchmarkCompletedCommandProjectionProbe(b *testing.B) {
	wc, jobs, exits, _ := completedOutputFixture(b)
	b.Run("current_durable_and_retained", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			completedProjectionSink = wc.commandCandidates("probe", jobs, exits)
		}
	})
	b.Run("durable_winners_only", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			completedProjectionSink = wc.commandCandidates("probe", nil, exits)
		}
	})
}

func BenchmarkCompletedTailCleaningProbe(b *testing.B) {
	_, _, exits, raw := completedOutputFixture(b)
	keys := []probeTailKey{{"probe", "epoch", "test-1", "r1"}, {"probe", "epoch", "test-2", "r2"}}
	for _, fixture := range []struct{ name, raw string }{{"durable_exit", exits[0].Output}, {"retained_job", raw}} {
		b.Run(fixture.name, func(b *testing.B) {
			raw := fixture.raw
			b.Run("clean_two_outputs_each_tick", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					for range keys {
						completedTailSink = cleanWorkTail(raw)
					}
				}
			})
			b.Run("exact_input_memo_two_hits", func(b *testing.B) {
				memo := probeTailMemo{}
				for _, key := range keys {
					memo.clean(key, raw)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					for _, key := range keys {
						completedTailSink = memo.clean(key, raw)
					}
				}
			})
		})
	}
}
