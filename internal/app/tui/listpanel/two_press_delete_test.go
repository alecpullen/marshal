package listpanel

import (
	"strings"
	"testing"
	"time"
)

// newDeleteList builds a list whose rows are deletable and whose identity is
// the item value ("t.b"), the way a key-bearing collection exposes it: the row
// survives a rebuild that reorders or inserts, so the cursor can follow it.
func newDeleteList(items *[]string) *FieldList {
	return NewFieldList(func() []*Field {
		out := make([]*Field, len(*items))
		for i, v := range *items {
			v := v
			out[i] = &Field{
				ID:     "t." + v,
				Title:  v,
				Kind:   KindScalar,
				GetStr: func() string { return v },
				SetStr: func(string) error { return nil },
				Del: func() {
					kept := make([]string, 0, len(*items))
					for _, s := range *items {
						if s != v {
							kept = append(kept, s)
						}
					}
					*items = kept
				},
			}
		}
		return out
	})
}

// TestDeleteFirstPressArmsInsteadOfDeleting pins AUD-13: d is a two-press
// gesture. The first press changes nothing and says what a second press would
// do; before this a single d deleted the row outright, undisclosed in the row
// hints.
func TestDeleteFirstPressArmsInsteadOfDeleting(t *testing.T) {
	items := []string{"a", "b", "c"}
	fl := newDeleteList(&items)
	fl.SetSize(60, 20)
	fl.SetCursor(1) // b

	fl.Update(kp("d"))

	if len(items) != 3 {
		t.Fatalf("the first d deleted (%v); it must only arm", items)
	}
	// The arm is loud: it renders under the cursor row, naming what a second
	// press would remove.
	view := fl.View()
	if !containsAll(view, "press d again", "to delete b") {
		t.Fatalf("the first d did not say what the second would do:\n%s", view)
	}
}

// TestDeleteSecondPressDeletesTheRow pins the confirmation half.
func TestDeleteSecondPressDeletesTheRow(t *testing.T) {
	items := []string{"a", "b", "c"}
	fl := newDeleteList(&items)
	fl.SetSize(60, 20)
	fl.SetCursor(1) // b

	fl.Update(kp("d"))
	fl.Update(kp("d"))

	if len(items) != 2 || items[0] != "a" || items[1] != "c" {
		t.Fatalf("the second d did not delete b: %v", items)
	}
	if got := len(fl.Rows()); got != 2 {
		t.Fatalf("row count = %d after the delete, want 2", got)
	}
}

// TestDeleteArmingIsCancelledByAnotherKey pins the escape hatch: an armed row
// must not survive the next keystroke, or a deliberate d later reads as a
// confirmation of a gesture the user has forgotten making.
func TestDeleteArmingIsCancelledByAnotherKey(t *testing.T) {
	items := []string{"a", "b", "c"}
	fl := newDeleteList(&items)
	fl.SetSize(60, 20)

	fl.Update(kp("d")) // arm b
	fl.Update(kp("j")) // move to c
	fl.Update(kp("k")) // back to b
	fl.Update(kp("d")) // must arm again, not delete

	if len(items) != 3 {
		t.Fatalf("a d after another key deleted (%v); it must arm again", items)
	}
	fl.Update(kp("d")) // now the confirmation
	if len(items) != 2 {
		t.Fatalf("the second consecutive d did not delete: %v", items)
	}
}

// TestDeleteOfAMiddleRowKeepsTheSelectionOnTheSameRow pins both halves of the
// repeat-press hazard: the cursor lands on the surviving neighbour, and a
// further d arms that neighbour rather than deleting it.
func TestDeleteOfAMiddleRowKeepsTheSelectionOnTheSameRow(t *testing.T) {
	items := []string{"a", "b", "c"}
	fl := newDeleteList(&items)
	fl.SetSize(60, 20)
	fl.SetCursor(1) // b

	fl.Update(kp("d"))
	fl.Update(kp("d")) // delete b

	if len(items) != 2 || items[0] != "a" || items[1] != "c" {
		t.Fatalf("deleting the middle row removed the wrong item: %v", items)
	}
	row := fl.CursorRow()
	if row == nil || row.Title != "c" {
		t.Fatalf("cursor is on %v after deleting the middle row, want the surviving neighbour c", row)
	}
	fl.Update(kp("d"))
	if len(items) != 2 {
		t.Fatalf("a d straight after the delete removed the neighbour too: %v", items)
	}
}

// TestCursorFollowsRowIdentityAcrossRefresh pins the positional-reconciliation
// defect: a refresh that inserts a row above the cursor must not drag the
// selection onto a different entry.
func TestCursorFollowsRowIdentityAcrossRefresh(t *testing.T) {
	items := []string{"a", "b", "c"}
	fl := newDeleteList(&items)
	fl.SetSize(60, 20)
	fl.SetCursor(1) // b

	// A row appears above the cursor, as a concurrent edit or a slow writer
	// would add one.
	items = append([]string{"z"}, items...)
	fl.Refresh()

	row := fl.CursorRow()
	if row == nil || row.Title != "b" {
		t.Fatalf("cursor is on %v after the insert, want b (same identity)", row)
	}
}

func TestDeleteArmingExpires(t *testing.T) {
	items := []string{"a", "b", "c"}
	fl := newDeleteList(&items)
	fl.SetSize(60, 20)
	now := fl.clock()
	fl.now = func() time.Time { return now }

	fl.Update(kp("d")) // arm
	now = now.Add(deleteArmWindow + time.Second)
	fl.Update(kp("d")) // must arm again, not delete

	if len(items) != 3 {
		t.Fatalf("an expired arm still deleted (%v)", items)
	}
	fl.Update(kp("d"))
	if len(items) != 2 {
		t.Fatalf("the second d inside the window did not delete: %v", items)
	}
}

func containsAll(hay string, needles ...string) bool {
	for _, n := range needles {
		if !strings.Contains(hay, n) {
			return false
		}
	}
	return true
}
