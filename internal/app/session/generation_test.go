package session

import (
	"testing"
)

func TestBeginGenerationRecordsLeafMessageID(t *testing.T) {
	s := newTestState()

	// No messages yet — StartMsgID should be 0 (replay everything).
	s.BeginGeneration("gen-1", 1, "digest-a")
	gen := s.Generation()
	if gen.ID != "gen-1" || gen.Seq != 1 || gen.SeedDigest != "digest-a" {
		t.Fatalf("Generation() = %+v, want {ID: gen-1, Seq: 1, SeedDigest: digest-a}", gen)
	}
	if gen.StartMsgID != 0 {
		t.Fatalf("StartMsgID = %d, want 0 (no messages yet)", gen.StartMsgID)
	}

	// Add a message, then begin a new generation.
	s.AddMessage(RoleUser, "hello", ContentTypePlain)
	msgID := s.Messages()[0].ID

	s.BeginGeneration("gen-2", 2, "digest-b")
	gen = s.Generation()
	if gen.ID != "gen-2" || gen.Seq != 2 || gen.SeedDigest != "digest-b" {
		t.Fatalf("Generation() = %+v, want {ID: gen-2, Seq: 2, SeedDigest: digest-b}", gen)
	}
	if gen.StartMsgID != msgID {
		t.Fatalf("StartMsgID = %d, want %d (leaf message ID)", gen.StartMsgID, msgID)
	}
}

func TestGenerationReturnsStoredBoundary(t *testing.T) {
	s := newTestState()

	// Default zero value.
	gen := s.Generation()
	if gen.ID != "" || gen.Seq != 0 || gen.SeedDigest != "" || gen.StartMsgID != 0 {
		t.Fatalf("default Generation() = %+v, want zero value", gen)
	}

	s.BeginGeneration("rollover-1", 5, "seed-abc")
	gen = s.Generation()
	if gen.ID != "rollover-1" || gen.Seq != 5 || gen.SeedDigest != "seed-abc" {
		t.Fatalf("Generation() = %+v, want {ID: rollover-1, Seq: 5, SeedDigest: seed-abc}", gen)
	}
}

func TestBeginGenerationWithMessagesRecordsLeaf(t *testing.T) {
	s := newTestState()
	s.AddMessage(RoleUser, "first", ContentTypePlain)
	s.AddMessage(RoleAssistant, "response", ContentTypeMarkdown)

	msgs := s.Messages()
	leafID := msgs[len(msgs)-1].ID

	s.BeginGeneration("gen-after-msgs", 3, "digest-c")
	gen := s.Generation()
	if gen.StartMsgID != leafID {
		t.Fatalf("StartMsgID = %d, want %d (last message ID)", gen.StartMsgID, leafID)
	}
}

func TestBeginGenerationOverwritesPrevious(t *testing.T) {
	s := newTestState()
	s.AddMessage(RoleUser, "hello", ContentTypePlain)
	firstLeaf := s.Messages()[0].ID

	s.BeginGeneration("first", 1, "d1")
	gen := s.Generation()
	if gen.StartMsgID != firstLeaf {
		t.Fatalf("first StartMsgID = %d, want %d", gen.StartMsgID, firstLeaf)
	}

	// Add more messages and begin a new generation.
	s.AddMessage(RoleAssistant, "world", ContentTypeMarkdown)
	secondLeaf := s.Messages()[1].ID

	s.BeginGeneration("second", 2, "d2")
	gen = s.Generation()
	if gen.ID != "second" || gen.Seq != 2 || gen.SeedDigest != "d2" {
		t.Fatalf("Generation() = %+v, want {ID: second, Seq: 2, SeedDigest: d2}", gen)
	}
	if gen.StartMsgID != secondLeaf {
		t.Fatalf("second StartMsgID = %d, want %d", gen.StartMsgID, secondLeaf)
	}
}
