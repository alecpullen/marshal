package session

import (
	"errors"
	"reflect"
	"testing"

	"marshal/internal/db"
)

func TestUpdateTodosRejectedUpdateLeavesMemoryAndPersistenceUntouched(t *testing.T) {
	d, sid := newStepTestDB(t)
	s := persistedState(t, d, sid)
	want := []db.TodoItem{{ID: "t9", Content: "original", Status: "pending"}}
	if err := s.SetTodos(want); err != nil {
		t.Fatal(err)
	}
	rejected := errors.New("rejected")
	err := s.UpdateTodos(func(current []db.TodoItem, floor int) ([]db.TodoItem, error) {
		if floor != 9 {
			t.Errorf("ID floor=%d, want 9", floor)
		}
		current[0].Content = "mutated copy"
		return current, rejected
	})
	if !errors.Is(err, rejected) {
		t.Fatal(err)
	}
	if got := s.Todos(); !reflect.DeepEqual(got, want) {
		t.Fatalf("rejected update changed memory: %v", got)
	}
	got, err := d.LoadTodos(sid)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("rejected update changed persistence: %v, %v", got, err)
	}
	// A rejected transaction must release serialization for a later update.
	if err := s.SetTodos(nil); err != nil {
		t.Fatal(err)
	}
}
