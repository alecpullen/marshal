package listpanel

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// NewCollectionFrame creates a Frame with add behavior: pressing a opens a
// key prompt (keyPrompt) and calls onAdd with the typed value.
func NewCollectionFrame(title, keyPrompt string, fields func() []*Field, onAdd func(string) error) *Frame {
	f := NewFrame(title, fields)
	f.List.KeyPrompt = keyPrompt
	f.List.OnAdd = onAdd
	return f
}

// PaneStack is one section's drill-down stack. Stack[0] is the section root.
type PaneStack struct {
	Stack  []*Frame
	width  int
	height int
}

func NewPaneStack(root *Frame) *PaneStack { return &PaneStack{Stack: []*Frame{root}} }

func (p *PaneStack) Top() *Frame { return p.Stack[len(p.Stack)-1] }

func (p *PaneStack) Push(f *Frame) {
	f.List.SetSize(p.width, p.height)
	p.Stack = append(p.Stack, f)
}

func (p *PaneStack) Pop() bool {
	if len(p.Stack) == 1 {
		return false
	}
	p.Stack = p.Stack[:len(p.Stack)-1]
	p.Top().List.Refresh()
	return true
}

func (p *PaneStack) SetSize(w, h int) {
	p.width, p.height = w, h
	for _, f := range p.Stack {
		f.List.SetSize(w, h)
	}
}

// Breadcrumb joins the section title with every pushed frame title:
// "MCP › github › Env".
func (p *PaneStack) Breadcrumb(sectionTitle string) string {
	parts := []string{sectionTitle}
	for _, f := range p.Stack[1:] {
		parts = append(parts, f.Title)
	}
	return strings.Join(parts, " › ")
}

func (p *PaneStack) Update(msg tea.Msg) tea.Cmd {
	cmd := p.Top().List.Update(msg)
	if f := p.Top().List.TakePushRequest(); f != nil {
		p.Push(f)
	}
	return cmd
}

func SortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// MapDrill is the generic key/value drill; parse/format adapt the value type.
func MapDrill[T any](id, title string, values *map[string]T, parse func(string) (T, error), format func(T) string) *Field {
	buildFields := func() []*Field {
		keys := SortedKeys(*values)
		out := make([]*Field, len(keys))
		for i, k := range keys {
			k := k
			out[i] = &Field{
				ID: id + "." + k, Title: k, Kind: KindScalar,
				Desc:   "value for key " + k,
				GetStr: func() string { return format((*values)[k]) },
				SetStr: func(v string) error {
					pv, err := parse(v)
					if err != nil {
						return err
					}
					(*values)[k] = pv
					return nil
				},
				Del: func() { delete(*values, k) },
			}
		}
		return out
	}
	return &Field{
		ID: id, Title: title, Kind: KindDrill,
		Summary: func() string { return fmt.Sprintf("%d entries", len(*values)) },
		Build: func() *Frame {
			return NewCollectionFrame(title, "New key", buildFields, func(k string) error {
				if k == "" {
					return fmt.Errorf("key cannot be empty")
				}
				if _, exists := (*values)[k]; exists {
					return fmt.Errorf("key already exists")
				}
				if *values == nil {
					*values = map[string]T{}
				}
				var zero T
				(*values)[k] = zero
				return nil
			})
		},
	}
}

func MapStringDrill(id, title string, values *map[string]string) *Field {
	return MapDrill(id, title, values, func(s string) (string, error) { return s, nil }, func(s string) string { return s })
}

func MapIntDrill(id, title string, values *map[string]int) *Field {
	return MapDrill(id, title, values,
		func(s string) (int, error) {
			v, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil {
				return 0, fmt.Errorf("must be a number")
			}
			return v, nil
		},
		strconv.Itoa)
}

type EntriesOpts struct {
	MoveUp   func(k string)
	MoveDown func(k string)
	Yank     func(k string) any
	Paste    func(k string, data any) error
}

func EntriesDrillExt(id, title, keyPrompt string, keys func() []string, rowTitle func(string) string,
	add func(string) error, buildEntry func(string) *Frame, del func(string), opts EntriesOpts) *Field {
	buildFields := func() []*Field {
		ks := keys()
		out := make([]*Field, len(ks))
		for i, k := range ks {
			k := k
			row := &Field{
				ID: id + "." + k, Title: rowTitle(k), Kind: KindDrill,
				Summary: func() string { return "" },
				Build:   func() *Frame { return buildEntry(k) },
				Del:     func() { del(k) },
			}
			if opts.MoveUp != nil {
				row.MoveUp = func() { opts.MoveUp(k) }
			}
			if opts.MoveDown != nil {
				row.MoveDown = func() { opts.MoveDown(k) }
			}
			if opts.Yank != nil {
				row.Yank = func() any { return opts.Yank(k) }
			}
			if opts.Paste != nil {
				row.Paste = func(data any) error { return opts.Paste(k, data) }
			}
			out[i] = row
		}
		return out
	}
	return &Field{
		ID: id, Title: title, Kind: KindDrill,
		Summary: func() string { return fmt.Sprintf("%d entries", len(keys())) },
		Build: func() *Frame {
			return NewCollectionFrame(title, keyPrompt, buildFields, add)
		},
	}
}

// EntriesDrill is a drill row over a named collection (providers, presets,
// MCP servers, hooks, permission rules). Each entry row drills again into
// buildEntry(key).
func EntriesDrill(id, title, keyPrompt string, keys func() []string, rowTitle func(string) string,
	add func(string) error, buildEntry func(string) *Frame, del func(string)) *Field {
	return EntriesDrillExt(id, title, keyPrompt, keys, rowTitle, add, buildEntry, del, EntriesOpts{})
}

type YankedMapEntry struct {
	Key string
	Val any
}

// ListDrill is a drill row over a []string: each item is an editable scalar
// row, a appends (typed value is the item), d deletes.
func ListDrill(id, title string, items *[]string) *Field {
	return ListDrillExt(id, title, items, EntriesOpts{})
}

func ListDrillExt(id, title string, items *[]string, opts EntriesOpts) *Field {
	buildFields := func() []*Field {
		out := make([]*Field, len(*items))
		for i := range *items {
			i := i
			row := &Field{
				ID: fmt.Sprintf("%s.%d", id, i), Title: (*items)[i], Kind: KindScalar,
				Desc:   "item in " + title,
				GetStr: func() string { return (*items)[i] },
				SetStr: func(v string) error {
					if v == "" {
						return fmt.Errorf("cannot be empty")
					}
					(*items)[i] = v
					return nil
				},
				Del: func() { *items = append((*items)[:i], (*items)[i+1:]...) },
			}
			if opts.MoveUp != nil {
				row.MoveUp = func() { opts.MoveUp(strconv.Itoa(i)) }
			}
			if opts.MoveDown != nil {
				row.MoveDown = func() { opts.MoveDown(strconv.Itoa(i)) }
			}
			if opts.Yank != nil {
				row.Yank = func() any { return opts.Yank(strconv.Itoa(i)) }
			}
			if opts.Paste != nil {
				row.Paste = func(data any) error { return opts.Paste(strconv.Itoa(i), data) }
			}
			out[i] = row
		}
		return out
	}
	return &Field{
		ID: id, Title: title, Kind: KindDrill,
		Summary: func() string { return fmt.Sprintf("%d items", len(*items)) },
		Build: func() *Frame {
			return NewCollectionFrame(title, "New entry", buildFields, func(v string) error {
				if strings.TrimSpace(v) == "" {
					return fmt.Errorf("cannot be empty")
				}
				*items = append(*items, v)
				return nil
			})
		},
	}
}
