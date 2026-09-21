package agent

import (
	"fmt"
	"strings"
	"testing"

	"marshal/internal/llm/schema"
)

// loopBlock builds a block of exactly n bytes whose shortest period is n
// itself, and whose interior has no repeated pattern. Both properties matter:
// the detector reasons about periods, so a block with a shorter period (or a
// filler that is itself periodic, which makes long suffixes periodic) would
// measure a different loop than the test name claims. The filler is a
// deterministic pseudo-random stream of the letters 'a'..'y'; the final byte is
// 'z', which appears nowhere else, so no proper border — and therefore no
// shorter period — can exist.
func loopBlock(n int) string {
	if n < 1 {
		panic("loopBlock: n must be positive")
	}
	var sb strings.Builder
	sb.Grow(n)
	state := uint32(0x9e3779b9)
	for sb.Len() < n-1 {
		state = state*1664525 + 1013904223
		sb.WriteByte(byte('a' + (state>>16)%25))
	}
	sb.WriteByte('z')
	return sb.String()
}

func TestLoopDetectorFiresAtExactThreshold(t *testing.T) {
	d := newLoopDetector()
	block := loopBlock(loopMinBlock)
	looped, repeated := d.feed(strings.Repeat(block, loopMinRepeats))
	if !looped {
		t.Fatalf("%d bytes repeated %d times must fire", loopMinBlock, loopMinRepeats)
	}
	if len(repeated) < loopMinBlock {
		t.Fatalf("repeated block = %d bytes, want >= %d", len(repeated), loopMinBlock)
	}
}

func TestLoopDetectorIgnoresSubThresholdBlock(t *testing.T) {
	d := newLoopDetector()
	block := loopBlock(loopMinBlock - 1)
	if looped, _ := d.feed(strings.Repeat(block, loopMinRepeats)); looped {
		t.Fatalf("%d-byte block must not fire", loopMinBlock-1)
	}
}

func TestLoopDetectorIgnoresSubBlockChain(t *testing.T) {
	// Four copies of a 150-byte block: 600 bytes of genuine repetition, but the
	// period is grown to two copies (300 bytes) to clear loopMinBlock, so three
	// such blocks need six copies. Four stays silent.
	d := newLoopDetector()
	block := loopBlock(loopMinBlock - 50)
	if looped, _ := d.feed(strings.Repeat(block, 4)); looped {
		t.Fatalf("four copies of a %d-byte block must not fire", len(block))
	}
}

func TestLoopDetectorFiresAcrossWhitespaceDrift(t *testing.T) {
	d := newLoopDetector()
	block := loopBlock(loopMinBlock + 20)
	// Same block three times, separated by different whitespace each time.
	looped, _ := d.feed(block + "\n\n" + block + "\n    " + block + "\n")
	if !looped {
		t.Fatal("whitespace-only drift must still count as verbatim repetition")
	}
}

func TestLoopDetectorFiresIncrementally(t *testing.T) {
	// A real stream arrives in small deltas; the detector must fire once the
	// third repetition has accumulated, not only on one big feed.
	d := newLoopDetector()
	block := loopBlock(loopMinBlock)
	fired := false
	for i := 0; i < loopMinRepeats+1 && !fired; i++ {
		for j := 0; j < len(block); j += 16 {
			end := j + 16
			if end > len(block) {
				end = len(block)
			}
			if looped, _ := d.feed(block[j:end]); looped {
				fired = true
				break
			}
		}
	}
	if !fired {
		t.Fatal("incremental deltas must reach the same verdict as one big feed")
	}
}

func TestLoopDetectorIgnoresVaryingReasoning(t *testing.T) {
	d := newLoopDetector()
	var sb strings.Builder
	for i := 0; i < 400; i++ {
		// Lengths and numbers grow, so no suffix is a repetition of anything.
		fmt.Fprintf(&sb, "step %d: checking input %d against case %d before moving on; ", i, i*7, i*i)
	}
	if looped, _ := d.feed(sb.String()); looped {
		t.Fatal("varied reasoning of the same shape must not fire")
	}
}

func TestLoopDetectorIgnoresShortRepeatedPhrase(t *testing.T) {
	d := newLoopDetector()
	if looped, _ := d.feed(strings.Repeat("let me check that. ", 9)); looped {
		t.Fatal("a sub-600-byte buffer must never fire")
	}
}

// TestLoopDetectorFiresOnGrownShortPhrase pins the period-growth rule on real
// prose. For a 19-byte phrase m = ceil(loopMinBlock/19) = 11, so the grown block
// is 209 bytes and confirmation needs loopMinRepeats*209 = 627 bytes = 33 copies:
// three copies of the *phrase* is nowhere near enough. 608 bytes is also a
// multiple of the period, so the short case isolates the threshold rather than
// the period rule.
func TestLoopDetectorFiresOnGrownShortPhrase(t *testing.T) {
	const phrase = "let me check that. "
	if len(phrase) != 19 {
		t.Fatalf("test setup: phrase is %d bytes, want 19", len(phrase))
	}
	short := newLoopDetector()
	if looped, _ := short.feed(strings.Repeat(phrase, 32)); looped {
		t.Fatal("32 copies (608 bytes) is below the grown 627-byte threshold and must stay silent")
	}
	looped, repeated := newLoopDetector().feed(strings.Repeat(phrase, 33))
	if !looped {
		t.Fatal("33 copies (627 bytes) must fire")
	}
	if len(repeated) < loopMinBlock {
		t.Fatalf("repeated block = %d bytes, want >= %d", len(repeated), loopMinBlock)
	}
}

func TestLoopDetectorIgnoresPeriodLongerThanAThirdOfWindow(t *testing.T) {
	// The window is kept as a tail, so a repetition whose period exceeds
	// loopWindow/loopMinRepeats cannot be confirmed. This is an accepted loss,
	// documented on the detector; the test pins it so a future window change is
	// a deliberate decision.
	d := newLoopDetector()
	block := loopBlock(loopWindow/loopMinRepeats + 100)
	if len(block)*loopMinRepeats <= loopWindow {
		t.Fatalf("test setup: block %d too small to exceed the window", len(block))
	}
	if looped, _ := d.feed(strings.Repeat(block, loopMinRepeats)); looped {
		t.Fatal("a period longer than a third of the window must not fire")
	}
}

func TestAppendNormalizedCollapsesWhitespace(t *testing.T) {
	got := appendNormalized("a ", "  \n\tb")
	if got != "a b" {
		t.Fatalf("appendNormalized = %q, want %q", got, "a b")
	}
}

func TestWithThinkingLoopNudgeDoesNotMutateInput(t *testing.T) {
	in := []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}
	out := withThinkingLoopNudge(in)
	if len(in) != 1 {
		t.Fatalf("input slice was mutated: len = %d, want 1", len(in))
	}
	if len(out) != 2 || out[1].Role != schema.RoleSystem || out[1].Content != thinkingLoopNudge {
		t.Fatalf("nudge not appended as a trailing system message: %#v", out)
	}
}
