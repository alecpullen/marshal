package settings

import (
	"fmt"
	"slices"
	"strconv"
)

// sliceOpts returns the move/yank/paste entriesOpts for a *[]T field.
// It replaces the per-type copies (hooks, permissions, string lists).
func sliceOpts[T any](items *[]T) entriesOpts {
	return entriesOpts{
		moveUp: func(k string) {
			i, _ := strconv.Atoi(k)
			if i <= 0 || i >= len(*items) {
				return
			}
			(*items)[i-1], (*items)[i] = (*items)[i], (*items)[i-1]
		},
		moveDown: func(k string) {
			i, _ := strconv.Atoi(k)
			if i < 0 || i >= len(*items)-1 {
				return
			}
			(*items)[i+1], (*items)[i] = (*items)[i], (*items)[i+1]
		},
		yank: func(k string) any {
			i, _ := strconv.Atoi(k)
			if i < 0 || i >= len(*items) {
				return nil
			}
			return (*items)[i]
		},
		paste: func(k string, data any) error {
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
