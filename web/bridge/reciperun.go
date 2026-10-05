package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// errNoStructuredResult is the result error when a recipe with a
// structured output ends without a parseable final json block.
const errNoStructuredResult = "gave up: no structured result"

// ReviewFinding is one finding of a review-findings result.
type ReviewFinding struct {
	Severity string `json:"severity"`
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	StepNode string `json:"stepNode,omitempty"`
}

// ReviewFindings is the review-findings structured output.
type ReviewFindings struct {
	Findings []ReviewFinding `json:"findings"`
	Summary  string          `json:"summary"`
}

// CIResult is the ci-result structured output.
type CIResult struct {
	Reproduced bool   `json:"reproduced"`
	Command    string `json:"command,omitempty"`
	Cause      string `json:"cause,omitempty"`
	Fixed      bool   `json:"fixed"`
	Notes      string `json:"notes,omitempty"`
}

// RecipeRunRequest asks for one run of a recipe.
type RecipeRunRequest struct {
	// Project is the project root. Ignored for a RepoID spawn, which
	// works on a fresh checkout.
	Project string
	// RepoID and Ref select a registered repo and the ref to check out.
	RepoID string
	Ref    string
	Inputs map[string]string
	// Origin defaults to OriginUI.
	Origin string
	// Routing and Limits, when set, override the recipe's own values.
	Routing json.RawMessage
	Limits  *RecipeLimits
	// OnDone, when set, is called once when the run ends.
	OnDone func(RecipeResult)
}

// RecipeResult is how a recipe run ended.
type RecipeResult struct {
	AgentID string
	// Output is the recipe's output kind.
	Output string
	// Parsed is a ReviewFindings or CIResult, per Output; nil otherwise.
	Parsed any
	// Err is empty on success.
	Err string
}

// render replaces each {{name}} in prompt with its input. Values are
// plain text: one containing "{{" is refused, so an input can never
// smuggle in a placeholder of its own.
func render(prompt string, inputs map[string]string, declared []RecipeInput) (string, error) {
	known := map[string]RecipeInput{}
	for _, in := range declared {
		known[in.Name] = in
	}
	for name, v := range inputs {
		if _, ok := known[name]; !ok {
			return "", fmt.Errorf("%w: unknown input %q", ErrInvalidRecipe, name)
		}
		if strings.Contains(v, "{{") {
			return "", fmt.Errorf("%w: input %q must not contain {{", ErrInvalidRecipe, name)
		}
	}
	for _, in := range declared {
		if in.Required && strings.TrimSpace(inputs[in.Name]) == "" {
			return "", fmt.Errorf("%w: input %q is required", ErrInvalidRecipe, in.Name)
		}
	}
	return placeholderRe.ReplaceAllStringFunc(prompt, func(m string) string {
		return inputs[placeholderRe.FindStringSubmatch(m)[1]]
	}), nil
}

// RunRecipe spawns an agent for a recipe, starts it, applies the limits,
// and reports the result through req.OnDone. It returns the agent id as
// soon as the run is under way.
func (f *Fleet) RunRecipe(ctx context.Context, name string, req RecipeRunRequest) (string, error) {
	rec, err := f.recipes.Get(name)
	if err != nil {
		return "", err
	}
	prompt, err := render(rec.Prompt, req.Inputs, rec.Inputs)
	if err != nil {
		return "", err
	}
	if err := f.budgetGate(""); err != nil {
		return "", err
	}
	origin := req.Origin
	if origin == "" {
		origin = OriginUI
	}
	routing := rec.Routing
	if len(req.Routing) > 0 {
		routing = req.Routing
	}
	limits := rec.Limits
	if req.Limits != nil {
		limits = req.Limits
	}
	title := rec.Title
	if title == "" {
		title = rec.Name
	}

	id, err := f.Spawn(ctx, req.Project, SpawnOptions{
		Name: title, Mode: rec.Mode, Isolated: true, Origin: origin,
		RepoID: req.RepoID, Ref: req.Ref, Routing: routing, Workspace: rec.Workspace,
	})
	if err != nil {
		if id != "" {
			f.discardFresh(id)
		}
		return "", err
	}
	fail := func(err error) (string, error) {
		f.discardFresh(id)
		return "", err
	}
	if a, ok := f.ws.Agent(id); ok {
		a.Recipe = rec.Name
		if err := f.ws.PutAgent(a); err != nil {
			return fail(err)
		}
	}

	var (
		stopDeadline func()
		maxMinutes   int
	)
	if limits != nil {
		if limits.MaxUSD > 0 {
			f.budgets.setAgentCap(id, limits.MaxUSD)
		}
		maxMinutes = limits.MaxMinutes
	}
	done := func(runErr error) {
		if stopDeadline != nil {
			stopDeadline()
		}
		f.budgets.clearAgentCap(id)
		res := f.recipeResult(id, rec.Output, runErr)
		if req.OnDone != nil {
			req.OnDone(res)
		}
	}

	rt, err := f.runtimeForAgent(id)
	if err != nil {
		return fail(err)
	}
	sid := f.sessionIDFor(rt)
	if maxMinutes > 0 {
		d := time.Duration(maxMinutes) * time.Minute
		fire := func() { f.workspaceTimeout(rt, sid) }
		if f.afterFunc != nil {
			stopDeadline = f.afterFunc(d, fire)
		} else {
			t := time.AfterFunc(d, fire)
			stopDeadline = func() { t.Stop() }
		}
	}

	bg := context.WithoutCancel(ctx)
	switch rec.Kind {
	case RecipePrompt:
		// Prompt blocks until the turn ends, which is the completion event.
		go func() { done(rt.reg.Prompt(bg, sid, prompt)) }()
	case RunSDD, RunSwarm:
		rr := RunRequest{Kind: rec.Kind}
		if rec.Kind == RunSDD {
			rr.Plan = prompt
		} else {
			rr.Goal = prompt
		}
		// The hook can fire before StartRun returns (a run that fails or
		// finishes within the dispatch grace). OnDone is only for runs that
		// were accepted, so hold the outcome until then.
		var (
			mu              sync.Mutex
			armed, finished bool
			finalErr        error
		)
		f.onRunDone(id, func(err error) {
			mu.Lock()
			finished, finalErr = true, err
			fire := armed
			mu.Unlock()
			if fire {
				done(err)
			}
		})
		if err := f.StartRun(ctx, id, rr); err != nil {
			f.clearRunDone(id)
			if stopDeadline != nil {
				stopDeadline()
			}
			f.budgets.clearAgentCap(id)
			return fail(err)
		}
		mu.Lock()
		armed = true
		fin, ferr := finished, finalErr
		mu.Unlock()
		if fin {
			go done(ferr)
		}
	}
	return id, nil
}

// recipeResult reads the agent's final message and parses the structured
// output the recipe asked for.
func (f *Fleet) recipeResult(agentID, output string, runErr error) RecipeResult {
	res := RecipeResult{AgentID: agentID, Output: output}
	if runErr != nil {
		res.Err = runErr.Error()
		return res
	}
	if output == "" || output == OutputNone {
		return res
	}
	rt, err := f.runtimeForAgent(agentID)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	raw, err := rt.reg.Stack(ctx, f.sessionIDFor(rt), 0)
	if err != nil {
		res.Err = "read result: " + err.Error()
		return res
	}
	parsed, err := parseRecipeOutput(output, lastFinalMessage(raw))
	if err != nil {
		slog.Default().Info("webbridge: recipe result not parsed", "agent", agentID, "err", err)
		res.Err = errNoStructuredResult
		return res
	}
	res.Parsed = parsed
	return res
}

// lastFinalMessage returns the content of the last "final" node of a
// session/stack snapshot.
func lastFinalMessage(stack json.RawMessage) string {
	var tree struct {
		Nodes []struct {
			Kind    string `json:"kind"`
			Message *struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"nodes"`
	}
	if json.Unmarshal(stack, &tree) != nil {
		return ""
	}
	for i := len(tree.Nodes) - 1; i >= 0; i-- {
		if n := tree.Nodes[i]; n.Kind == "final" && n.Message != nil {
			return n.Message.Content
		}
	}
	return ""
}

// lastJSONBlock returns the body of the last ```json fenced block in text.
func lastJSONBlock(text string) (string, bool) {
	const open = "```json"
	start := strings.LastIndex(text, open)
	for start >= 0 {
		body := text[start+len(open):]
		if nl := strings.IndexByte(body, '\n'); nl >= 0 {
			if end := strings.Index(body[nl+1:], "```"); end >= 0 {
				return strings.TrimSpace(body[nl+1 : nl+1+end]), true
			}
		}
		// An unterminated block: try the one before it.
		start = strings.LastIndex(text[:start], open)
	}
	return "", false
}

var errBadOutput = errors.New("no valid structured output")

func parseRecipeOutput(output, message string) (any, error) {
	block, ok := lastJSONBlock(message)
	if !ok {
		return nil, errBadOutput
	}
	switch output {
	case OutputReviewFindings:
		var rf ReviewFindings
		if json.Unmarshal([]byte(block), &rf) != nil || rf.Findings == nil {
			return nil, errBadOutput
		}
		for _, fd := range rf.Findings {
			switch fd.Severity {
			case "blocking", "should-fix", "nit":
			default:
				return nil, errBadOutput
			}
		}
		return rf, nil
	case OutputCIResult:
		var raw map[string]json.RawMessage
		if json.Unmarshal([]byte(block), &raw) != nil {
			return nil, errBadOutput
		}
		if _, ok := raw["reproduced"]; !ok {
			return nil, errBadOutput
		}
		var cr CIResult
		if json.Unmarshal([]byte(block), &cr) != nil {
			return nil, errBadOutput
		}
		return cr, nil
	}
	return nil, errBadOutput
}

// onRunDone registers fn to run once when the next run dispatched on the
// agent ends, with the run's final error (nil on success).
func (f *Fleet) onRunDone(agentID string, fn func(error)) {
	f.runDoneMu.Lock()
	defer f.runDoneMu.Unlock()
	if f.runDone == nil {
		f.runDone = map[string]func(error){}
	}
	f.runDone[agentID] = fn
}

func (f *Fleet) clearRunDone(agentID string) {
	f.runDoneMu.Lock()
	delete(f.runDone, agentID)
	f.runDoneMu.Unlock()
}

func (f *Fleet) fireRunDone(agentID string, err error) {
	f.runDoneMu.Lock()
	fn := f.runDone[agentID]
	delete(f.runDone, agentID)
	f.runDoneMu.Unlock()
	if fn != nil {
		fn(err)
	}
}
