package session

import (
	"testing"
)

func TestBeginGenerationRecordsLeafMessageID(t *testing.T) {
	s := newTestState()

	// Add some messages so there is a leaf.
	s.AddMessage(RoleUser, "hello", ContentTypePlain)
	s.AddMessage(RoleAssistant, "hi", ContentTypePlain)

	msgs := s.Messages()
	leafID := msgs[len(msgs)-1].ID

	s.BeginGeneration("gen-1", 1, "abc123")

	gen := s.Generation()
	if gen.ID != "gen-1" {
		t.Fatalf("Generation().ID = %q, want %q", gen.ID, "gen-1")
	}
	if gen.Seq != 1 {
		t.Fatalf("Generation().Seq = %d, want 1", gen.Seq)
	}
	if gen.SeedDigest != "abc123" {
		t.Fatalf("Generation().SeedDigest = %q, want %q", gen.SeedDigest, "abc123")
	}
	if gen.StartMsgID != leafID {
		t.Fatalf("Generation().StartMsgID = %d, want %d (leaf message ID)", gen.StartMsgID, leafID)
	}
}

func TestBeginGenerationZeroStartMsgIDWhenNoMessages(t *testing.T) {
	s := newTestState()

	// No messages added — StartMsgID must be 0 (replay everything).
	s.BeginGeneration("gen-0", 0, "seed")

	gen := s.Generation()
	if gen.StartMsgID != 0 {
		t.Fatalf("Generation().StartMsgID = %d, want 0 (no messages)", gen.StartMsgID)
	}
}

func TestGenerationReturnsStoredBoundary(t *testing.T) {
	s := newTestState()

	s.AddMessage(RoleUser, "turn1", ContentTypePlain)
	s.BeginGeneration("gen-a", 2, "digest-a")

	gen := s.Generation()
	if gen.ID != "gen-a" || gen.Seq != 2 || gen.SeedDigest != "digest-a" {
		t.Fatalf("Generation() = %+v, want {ID: gen-a, Seq: 2, SeedDigest: digest-a}", gen)
	}

	// Overwrite with a new generation.
	s.AddMessage(RoleUser, "turn2", ContentTypePlain)
	s.BeginGeneration("gen-b", 3, "digest-b")

	gen = s.Generation()
	if gen.ID != "gen-b" || gen.Seq != 3 || gen.SeedDigest != "digest-b" {
		t.Fatalf("Generation() after second call = %+v, want {ID: gen-b, Seq: 3, SeedDigest: digest-b}", gen)
	}
}

func TestGenerationDefaultIsZeroValue(t *testing.T) {
	s := newTestState()

	gen := s.Generation()
	if gen.ID != "" || gen.Seq != 0 || gen.SeedDigest != "" || gen.StartMsgID != 0 {
		t.Fatalf("Generation() before BeginGeneration = %+v, want zero value", gen)
	}
}

func TestGenerationMultipleCallsConsistent(t *testing.T) {
	s := newTestState()
	s.AddMessage(RoleUser, "msg1", ContentTypePlain)
	s.AddMessage(RoleAssistant, "msg2", ContentTypeMarkdown)
	leafID := s.Messages()[1].ID

	s.BeginGeneration("multi", 7, "digest-multi")

	// Multiple calls to Generation() must return the same value.
	for i := 0; i < 5; i++ {
		gen := s.Generation()
		if gen.ID != "multi" {
			t.Fatalf("call %d: ID = %q, want %q", i, gen.ID, "multi")
		}
		if gen.Seq != 7 {
			t.Fatalf("call %d: Seq = %d, want %d", i, gen.Seq, 7)
		}
		if gen.SeedDigest != "digest-multi" {
			t.Fatalf("call %d: SeedDigest = %q, want %q", i, gen.SeedDigest, "digest-multi")
		}
		if gen.StartMsgID != leafID {
			t.Fatalf("call %d: StartMsgID = %d, want %d", i, gen.StartMsgID, leafID)
		}
	}
}

func TestBeginGenerationConcurrentSafe(t *testing.T) {
	s := newTestState()
	s.AddMessage(RoleUser, "base", ContentTypePlain)

	done := make(chan struct{})
	const goroutines = 10

	for i := 0; i < goroutines; i++ {
		go func(i int) {
			id := "gen-concurrent"
			seq := i
			digest := "digest-concurrent"
			s.BeginGeneration(id, seq, digest)
			gen := s.Generation()
			// We only verify that the call does not panic or deadlock.
			_ = gen
			done <- struct{}{}
		}(i)
	}

	for i := 0; i < goroutines; i++ {
		<-done
	}

	// After all goroutines, the generation should be in a valid state.
	gen := s.Generation()
	if gen.ID != "gen-concurrent" {
		t.Fatalf("final ID = %q, want %q", gen.ID, "gen-concurrent")
	}
	if gen.StartMsgID == 0 {
		t.Fatal("StartMsgID should be non-zero (messages existed)")
	}
}
