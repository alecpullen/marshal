package viewmodel

import (
	"strings"
	"time"
)

// Find does a depth-first search for the node whose ID key is key and returns
// it with its parent. A turn's parent is nil.
func Find(turns []*Node, key string) (n *Node, parent *Node) {
	var walk func(nodes []*Node, p *Node) (*Node, *Node)
	walk = func(nodes []*Node, p *Node) (*Node, *Node) {
		for _, c := range nodes {
			if c.ID.Key == key {
				return c, p
			}
			if f, fp := walk(c.Children, c); f != nil {
				return f, fp
			}
		}
		return nil, nil
	}
	return walk(turns, nil)
}

// StepDiff is the diff a step produced, for the review page's by-step view.
type StepDiff struct {
	StepNode, TurnNode, TaskNode, Headline string
	At                                     time.Time
	Files                                  []string
	Diff                                   string
}

// StepDiffs lists, in tree order, every step that made a diff-tool call.
func StepDiffs(turns []*Node) []StepDiff {
	var out []StepDiff
	for _, turn := range turns {
		var walk func(n *Node, task string)
		walk = func(n *Node, task string) {
			switch n.Kind {
			case KindTask:
				task = n.ID.Key
			case KindStep:
				if d, ok := stepDiff(n); ok {
					d.TurnNode, d.TaskNode = turn.ID.Key, task
					out = append(out, d)
				}
				return
			}
			for _, c := range n.Children {
				walk(c, task)
			}
		}
		for _, c := range turn.Children {
			walk(c, "")
		}
	}
	return out
}

func stepDiff(step *Node) (StepDiff, bool) {
	var d StepDiff
	var diffs []string
	seen := map[string]bool{}
	found := false
	for _, row := range step.Children {
		for _, ev := range row.Tools {
			if !isDiffTool(ev.ToolName) {
				continue
			}
			if !found {
				d.At = ev.Timestamp
				found = true
			}
			diffs = append(diffs, ev.ResultContent)
			for _, f := range ev.FilesChanged {
				if !seen[f] {
					seen[f] = true
					d.Files = append(d.Files, f)
				}
			}
		}
	}
	if !found {
		return d, false
	}
	d.StepNode = step.ID.Key
	d.Diff = strings.Join(diffs, "\n")
	if step.Step != nil {
		d.Headline, _, _ = StepHeadline(step.Step, step.Children)
	}
	return d, true
}
