package native

import (
	"bytes"
	"sync"
	"testing"
)

func TestBoundedOutputRetainsPrefixAndReportsTruncation(t *testing.T) {
	b := NewBoundedOutput(5, nil)
	n, err := b.Write([]byte("abc"))
	if n != 3 || err != nil {
		t.Fatalf("Write(abc) = (%d, %v), want (3, nil)", n, err)
	}
	if b.Truncated() {
		t.Fatal("Truncated() = true after first write that fits")
	}

	n, err = b.Write([]byte("def"))
	if n != 3 || err != nil {
		t.Fatalf("Write(def) = (%d, %v), want (3, nil)", n, err)
	}
	if !b.Truncated() {
		t.Fatal("Truncated() = false after overflow")
	}
	if got := b.String(); got != "abcde" {
		t.Fatalf("String() = %q, want %q", got, "abcde")
	}
}

func TestBoundedOutputForwardsOnlyRetainedBytes(t *testing.T) {
	var obs bytes.Buffer
	b := NewBoundedOutput(5, &obs)
	_, _ = b.Write([]byte("abcdefgh"))
	if got := b.String(); got != "abcde" {
		t.Fatalf("String() = %q, want %q", got, "abcde")
	}
	if obs.String() != "abcde" {
		t.Fatalf("observer content = %q, want %q", obs.String(), "abcde")
	}
}

func TestBoundedOutputConcurrentSnapshot(t *testing.T) {
	b := NewBoundedOutput(100, nil)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, _ = b.Write([]byte("hello world\n"))
				_ = b.String()
				_ = b.Truncated()
			}
		}()
	}
	wg.Wait()
	if len(b.String()) > 100 {
		t.Fatalf("String() len = %d, want <= 100", len(b.String()))
	}
}

func TestOutputLimitUsesDefault(t *testing.T) {
	if got := OutputLimit(-1); got != defaultMaxOutputBytes {
		t.Fatalf("OutputLimit(-1) = %d, want %d", got, defaultMaxOutputBytes)
	}
	if got := OutputLimit(0); got != defaultMaxOutputBytes {
		t.Fatalf("OutputLimit(0) = %d, want %d", got, defaultMaxOutputBytes)
	}
	if got := OutputLimit(5000); got != 5000 {
		t.Fatalf("OutputLimit(5000) = %d, want 5000", got)
	}
}

func TestBoundedOutputEmptyWrite(t *testing.T) {
	b := NewBoundedOutput(5, nil)
	n, err := b.Write(nil)
	if n != 0 || err != nil {
		t.Fatalf("Write(nil) = (%d, %v), want (0, nil)", n, err)
	}
	if b.Truncated() {
		t.Fatal("Truncated() = true after empty write")
	}
}
