package settings

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
)

func newFrame(title string, fields func() []*field) *frame {
	return &frame{title: title, list: newFieldList(fields)}
}

func newCollectionFrame(title, keyPrompt string, fields func() []*field, onAdd func(string) error) *frame {
	f := newFrame(title, fields)
	f.list.keyPrompt = keyPrompt
	f.list.onAdd = onAdd
	return f
}

// paneStack is one section's drill-down stack. stack[0] is the section root.
type paneStack struct {
	stack  []*frame
	width  int
	height int
}

// PaneStack is an exported alias for paneStack.
type PaneStack = paneStack

func newPaneStack(root *frame) *paneStack { return &paneStack{stack: []*frame{root}} }

func (p *paneStack) top() *frame { return p.stack[len(p.stack)-1] }

func (p *paneStack) push(f *frame) {
	f.list.SetSize(p.width, p.height)
	p.stack = append(p.stack, f)
}

func (p *paneStack) pop() bool {
	if len(p.stack) == 1 {
		return false
	}
	p.stack = p.stack[:len(p.stack)-1]
	p.top().list.Refresh()
	return true
}

func (p *paneStack) SetSize(w, h int) {
	p.width, p.height = w, h
	for _, f := range p.stack {
		f.list.SetSize(w, h)
	}
}

// breadcrumb joins the section title with every pushed frame title:
// "MCP › github › Env".
func (p *paneStack) breadcrumb(sectionTitle string) string {
	parts := []string{sectionTitle}
	for _, f := range p.stack[1:] {
		parts = append(parts, f.title)
	}
	return strings.Join(parts, " › ")
}

func (p *paneStack) Update(msg tea.Msg) tea.Cmd {
	cmd := p.top().list.Update(msg)
	if f := p.top().list.TakePushRequest(); f != nil {
		p.push(f)
	}
	return cmd
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// mapDrill is the generic key/value drill; parse/format adapt the value type.
func mapDrill[T any](id, title string, values *map[string]T, parse func(string) (T, error), format func(T) string) *field {
	buildFields := func() []*field {
		keys := sortedKeys(*values)
		out := make([]*field, len(keys))
		for i, k := range keys {
			k := k
			out[i] = &field{
				id: id + "." + k, title: k, kind: kindScalar,
				desc:   "value for key " + k,
				getStr: func() string { return format((*values)[k]) },
				setStr: func(v string) error {
					pv, err := parse(v)
					if err != nil {
						return err
					}
					(*values)[k] = pv
					return nil
				},
				del: func() { delete(*values, k) },
			}
		}
		return out
	}
	return &field{
		id: id, title: title, kind: kindDrill,
		summary: func() string { return fmt.Sprintf("%d entries", len(*values)) },
		build: func() *frame {
			return newCollectionFrame(title, "New key", buildFields, func(k string) error {
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

func mapStringDrill(id, title string, values *map[string]string) *field {
	return mapDrill(id, title, values, func(s string) (string, error) { return s, nil }, func(s string) string { return s })
}

func mapIntDrill(id, title string, values *map[string]int) *field {
	return mapDrill(id, title, values,
		func(s string) (int, error) {
			v, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil {
				return 0, fmt.Errorf("must be a number")
			}
			return v, nil
		},
		strconv.Itoa)
}

type entriesOpts struct {
	moveUp   func(k string)
	moveDown func(k string)
	yank     func(k string) any
	paste    func(k string, data any) error
}

func entriesDrillExt(id, title, keyPrompt string, keys func() []string, rowTitle func(string) string,
	add func(string) error, buildEntry func(string) *frame, del func(string), opts entriesOpts) *field {
	buildFields := func() []*field {
		ks := keys()
		out := make([]*field, len(ks))
		for i, k := range ks {
			k := k
			row := &field{
				id: id + "." + k, title: rowTitle(k), kind: kindDrill,
				summary: func() string { return "" },
				build:   func() *frame { return buildEntry(k) },
				del:     func() { del(k) },
			}
			if opts.moveUp != nil {
				row.moveUp = func() { opts.moveUp(k) }
			}
			if opts.moveDown != nil {
				row.moveDown = func() { opts.moveDown(k) }
			}
			if opts.yank != nil {
				row.yank = func() any { return opts.yank(k) }
			}
			if opts.paste != nil {
				row.paste = func(data any) error { return opts.paste(k, data) }
			}
			out[i] = row
		}
		return out
	}
	return &field{
		id: id, title: title, kind: kindDrill,
		summary: func() string { return fmt.Sprintf("%d entries", len(keys())) },
		build: func() *frame {
			return newCollectionFrame(title, keyPrompt, buildFields, add)
		},
	}
}

// entriesDrill is a drill row over a named collection (providers, presets,
// MCP servers, hooks, permission rules). Each entry row drills again into
// buildEntry(key).
func entriesDrill(id, title, keyPrompt string, keys func() []string, rowTitle func(string) string,
	add func(string) error, buildEntry func(string) *frame, del func(string)) *field {
	return entriesDrillExt(id, title, keyPrompt, keys, rowTitle, add, buildEntry, del, entriesOpts{})
}

type yankedMapEntry struct {
	key string
	val any
}

// listDrill is a drill row over a []string: each item is an editable scalar
// row, a appends (typed value is the item), d deletes.
func listDrill(id, title string, items *[]string) *field {
	return listDrillExt(id, title, items, entriesOpts{})
}

func listDrillExt(id, title string, items *[]string, opts entriesOpts) *field {
	buildFields := func() []*field {
		out := make([]*field, len(*items))
		for i := range *items {
			i := i
			row := &field{
				id: fmt.Sprintf("%s.%d", id, i), title: (*items)[i], kind: kindScalar,
				desc:   "item in " + title,
				getStr: func() string { return (*items)[i] },
				setStr: func(v string) error {
					if v == "" {
						return fmt.Errorf("cannot be empty")
					}
					(*items)[i] = v
					return nil
				},
				del: func() { *items = append((*items)[:i], (*items)[i+1:]...) },
			}
			if opts.moveUp != nil {
				row.moveUp = func() { opts.moveUp(strconv.Itoa(i)) }
			}
			if opts.moveDown != nil {
				row.moveDown = func() { opts.moveDown(strconv.Itoa(i)) }
			}
			if opts.yank != nil {
				row.yank = func() any { return opts.yank(strconv.Itoa(i)) }
			}
			if opts.paste != nil {
				row.paste = func(data any) error { return opts.paste(strconv.Itoa(i), data) }
			}
			out[i] = row
		}
		return out
	}
	return &field{
		id: id, title: title, kind: kindDrill,
		summary: func() string { return fmt.Sprintf("%d items", len(*items)) },
		build: func() *frame {
			return newCollectionFrame(title, "New entry", buildFields, func(v string) error {
				if strings.TrimSpace(v) == "" {
					return fmt.Errorf("cannot be empty")
				}
				*items = append(*items, v)
				return nil
			})
		},
	}
}
