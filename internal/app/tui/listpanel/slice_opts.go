package listpanel

import (
	"fmt"
	"slices"
	"strconv"
)

// SliceOpts returns the move/yank/paste EntriesOpts for a *[]T field.
// It replaces the per-type copies (hooks, permissions, string lists).
func SliceOpts[T any](items *[]T) EntriesOpts {
	return EntriesOpts{
		MoveUp: func(k string) {
			i, _ := strconv.Atoi(k)
			if i <= 0 || i >= len(*items) {
				return
			}
			(*items)[i-1], (*items)[i] = (*items)[i], (*items)[i-1]
		},
		MoveDown: func(k string) {
			i, _ := strconv.Atoi(k)
			if i < 0 || i >= len(*items)-1 {
				return
			}
			(*items)[i+1], (*items)[i] = (*items)[i], (*items)[i+1]
		},
		Yank: func(k string) any {
			i, _ := strconv.Atoi(k)
			if i < 0 || i >= len(*items) {
				return nil
			}
			return (*items)[i]
		},
		Paste: func(k string, data any) error {
			v, ok := data.(T)
			if !ok {
				return fmt.Errorf("nothing yanked")
			}
			i, _ := strconv.Atoi(k)
			*items = slices.Insert(*items, min(i+1, len(*items)), v)
			return nil
		},
	}
}
