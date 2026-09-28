package layout

import "testing"

func TestRectEmptyAndEdges(t *testing.T) {
	r := Rect{X: 2, Y: 3, Width: 10, Height: 4}
	if r.Empty() {
		t.Fatal("a 10x4 rect must not be empty")
	}
	if r.Right() != 12 || r.Bottom() != 7 {
		t.Errorf("Right/Bottom = %d/%d, want 12/7", r.Right(), r.Bottom())
	}
	for _, tc := range []struct {
		name string
		r    Rect
	}{
		{"zero width", Rect{Y: 1, Height: 3}},
		{"zero height", Rect{X: 1, Width: 3}},
		{"negative width", Rect{Width: -1, Height: 3}},
		{"negative height", Rect{Width: 3, Height: -1}},
		{"zero value", Rect{}},
	} {
		if !tc.r.Empty() {
			t.Errorf("%s: Empty() = false, want true", tc.name)
		}
	}
}

func TestRectContainsIsHalfOpen(t *testing.T) {
	r := Rect{X: 2, Y: 3, Width: 10, Height: 4}
	inside := []struct{ x, y int }{{2, 3}, {11, 6}, {5, 4}}
	for _, p := range inside {
		if !r.Contains(p.x, p.y) {
			t.Errorf("Contains(%d,%d) = false, want true", p.x, p.y)
		}
	}
	// The right and bottom edges are exclusive.
	outside := []struct{ x, y int }{{1, 3}, {12, 3}, {2, 2}, {2, 7}, {-1, 3}, {2, -1}}
	for _, p := range outside {
		if r.Contains(p.x, p.y) {
			t.Errorf("Contains(%d,%d) = true, want false", p.x, p.y)
		}
	}
	if (Rect{}).Contains(0, 0) {
		t.Error("an empty rect must contain nothing")
	}
}

func TestRectRow(t *testing.T) {
	r := Rect{X: 0, Y: 5, Width: 10, Height: 3}
	for y, want := range map[int]int{5: 0, 6: 1, 7: 2} {
		got, ok := r.Row(y)
		if !ok || got != want {
			t.Errorf("Row(%d) = %d, %v; want %d, true", y, got, ok, want)
		}
	}
	for _, y := range []int{4, 8, -1} {
		if _, ok := r.Row(y); ok {
			t.Errorf("Row(%d) reported inside the rect", y)
		}
	}
	if _, ok := (Rect{}).Row(0); ok {
		t.Error("an empty rect has no rows")
	}
}

func TestFrameBodySpansBetweenTopBarAndComposer(t *testing.T) {
	f := Frame{
		Width:      160,
		Height:     40,
		TopBar:     Rect{X: 0, Y: 0, Width: 160, Height: 1},
		Composer:   Rect{X: 0, Y: 36, Width: 160, Height: 3},
		Footer:     Rect{X: 0, Y: 39, Width: 160, Height: 1},
		Transcript: Rect{X: 0, Y: 1, Width: 120, Height: 35},
	}
	body := f.Body()
	if body != (Rect{X: 0, Y: 1, Width: 160, Height: 35}) {
		t.Errorf("Body() = %+v, want {0 1 160 35}", body)
	}
}

func TestFrameBodyWithoutTopBar(t *testing.T) {
	f := Frame{
		Width:    80,
		Height:   24,
		Composer: Rect{X: 0, Y: 21, Width: 80, Height: 2},
		Footer:   Rect{X: 0, Y: 23, Width: 80, Height: 1},
	}
	if got := f.Body(); got != (Rect{X: 0, Y: 0, Width: 80, Height: 21}) {
		t.Errorf("Body() = %+v, want {0 0 80 21}", got)
	}
}

// TestFrameBodyDegenerate: a frame with no room between the top bar and the
// composer reports an empty body rather than a negative-height rectangle.
func TestFrameBodyDegenerate(t *testing.T) {
	f := Frame{
		Width:    80,
		Height:   24,
		TopBar:   Rect{X: 0, Y: 0, Width: 80, Height: 10},
		Composer: Rect{X: 0, Y: 5, Width: 80, Height: 2},
	}
	if got := f.Body(); !got.Empty() {
		t.Errorf("Body() = %+v, want an empty rect", got)
	}
	if got := (Frame{}).Body(); !got.Empty() {
		t.Errorf("zero Frame Body() = %+v, want an empty rect", got)
	}
}
