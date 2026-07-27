package listpanel

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestListDrillEditsSlice(t *testing.T) {
	items := []string{"rm -rf", "git push --force"}
	root := NewFrame("Shell", func() []*Field {
		return []*Field{ListDrill("shell.deny", "Deny patterns", &items)}
	})
	ps := NewPaneStack(root)
	ps.Top().List.SetSize(60, 20)

	// summary row shows the count
	if !strings.Contains(ps.Top().List.View(), "2 items") {
		t.Fatalf("expected item count summary, got:\n%s", ps.Top().List.View())
	}

	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // drill in
	if len(ps.Stack) != 2 {
		t.Fatalf("enter should push the list frame, depth=%d", len(ps.Stack))
	}
	if got := ps.Breadcrumb("Shell"); got != "Shell › Deny patterns" {
		t.Fatalf("breadcrumb wrong: %q", got)
	}

	// add an item: a → typed value → enter
	ps.Update(kp("a"))
	for _, r := range "curl" {
		ps.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(items) != 3 || items[2] != "curl" {
		t.Fatalf("add should append, got %v", items)
	}

	// delete the first item
	ps.Top().List.SetCursor(0)
	ps.Update(kp("d"))
	if len(items) != 2 || items[0] != "git push --force" {
		t.Fatalf("d should delete row 0, got %v", items)
	}

	// pop back to root
	if !ps.Pop() {
		t.Fatal("pop should succeed above root")
	}
	if ps.Pop() {
		t.Fatal("pop at root must return false")
	}
}

func TestMapIntDrillEditsValues(t *testing.T) {
	m := map[string]int{"reviewer": 4}
	root := NewFrame("Swarm", func() []*Field {
		return []*Field{MapIntDrill("swarm.tool_iters", "Tool iters", &m)}
	})
	ps := NewPaneStack(root)
	ps.Top().List.SetSize(60, 20)
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // drill

	// add key then edit its value
	ps.Update(kp("a"))
	for _, r := range "planner" {
		ps.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if _, ok := m["planner"]; !ok {
		t.Fatalf("add should create the key, got %v", m)
	}
	// cursor lands on the new row; enter opens the value edit
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	for _, r := range "7" {
		ps.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m["planner"] != 7 {
		t.Fatalf("value edit should apply 7, got %v", m)
	}
}

func TestEntriesDrillBuildsSubFrame(t *testing.T) {
	vals := map[string]string{"local": "ollama"}
	root := NewFrame("Providers", func() []*Field {
		return []*Field{EntriesDrill("providers", "Providers", "New provider name",
			func() []string { return SortedKeys(vals) },
			func(k string) string { return k },
			func(k string) error { vals[k] = ""; return nil },
			func(k string) *Frame {
				return NewFrame(k, func() []*Field {
					v := k
					return []*Field{ScalarField("providers."+k+".type", "Type",
						func() string { return vals[v] },
						func(s string) error { vals[v] = s; return nil })}
				})
			},
			func(k string) { delete(vals, k) })}
	})
	ps := NewPaneStack(root)
	ps.Top().List.SetSize(60, 20)
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // drill into collection
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // drill into "local"
	if len(ps.Stack) != 3 {
		t.Fatalf("expected depth 3, got %d", len(ps.Stack))
	}
	if got := ps.Breadcrumb("Providers"); got != "Providers › Providers › local" {
		t.Fatalf("breadcrumb wrong: %q", got)
	}
}
