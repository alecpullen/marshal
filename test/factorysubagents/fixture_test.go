package factorysubagents

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// The scripted provider
// ---------------------------------------------------------------------------

// workerModel is the one model the fixture provider advertises and the one the
// worker session's config names. Nothing here reaches a real endpoint: the
// config the child reads points its provider at this test's own loopback
// server, which serves exactly the replies it was built with and refuses
// everything else.
const workerModel = "fixture-worker-model"

// chatMessage is one message a chat request carries.
type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"tool_call_id"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// text flattens a message's content, which the client sends either as a plain
// string or as an array of typed parts.
func (m chatMessage) text() string {
	if len(m.Content) == 0 {
		return ""
	}
	var plain string
	if json.Unmarshal(m.Content, &plain) == nil {
		return plain
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, part := range parts {
		b.WriteString(part.Text)
	}
	return b.String()
}

// chatRequest is the part of a chat completion request this fixture reads.
type chatRequest struct {
	Model    string        `json:"model"`
	Stream   bool          `json:"stream"`
	Messages []chatMessage `json:"messages"`
	Tools    []struct {
		Function struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"function"`
	} `json:"tools"`
}

func (r chatRequest) toolNames() map[string]bool {
	out := make(map[string]bool, len(r.Tools))
	for _, tool := range r.Tools {
		out[tool.Function.Name] = true
	}
	return out
}

// toolDescription is one offered tool's description, which is where the
// runtime states the bounds the session enforces.
func (r chatRequest) toolDescription(name string) string {
	for _, tool := range r.Tools {
		if tool.Function.Name == name {
			return tool.Function.Description
		}
	}
	return ""
}

// systemText is every system message's text, concatenated.
func (r chatRequest) systemText() string {
	var b strings.Builder
	for _, m := range r.Messages {
		if m.Role == "system" {
			b.WriteString(m.text())
			b.WriteString("\n")
		}
	}
	return b.String()
}

// lastUserText is the newest user message's text: the prompt the turn was
// started with, or the continuation prompt of a later turn.
func (r chatRequest) lastUserText() string {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == "user" {
			return r.Messages[i].text()
		}
	}
	return ""
}

// requestKind names which part of the script a request is. Every reply the
// fixture serves is chosen from the request itself, so a request that does not
// match the script is refused rather than answered with a plausible-looking
// fabrication.
type requestKind string

const (
	// kindImplement is the implementer worker's own turn: the factory's
	// implement prompt, with no delegation attempted yet.
	kindImplement requestKind = "implement"
	// kindSecondDelegate is the step after the first delegation returned: a
	// second agent.run that the approved concurrency cap must refuse.
	kindSecondDelegate requestKind = "second-delegate"
	// kindEarlyReport is the step after that refusal: the final answer the
	// turn writes while the child it started is still running.
	kindEarlyReport requestKind = "early-report"
	// kindChild is an agent.run child: its prompt is the delegated-subtask
	// one and it has no agent.run of its own.
	kindChild requestKind = "child"
	// kindContinuation is the second turn of the same effect, asked for once
	// the child settled.
	kindContinuation requestKind = "continuation"
	// kindReview is the independent reviewer worker's turn.
	kindReview requestKind = "review"
)

// recordedRequest is one request the fixture served, kept so the test can
// assert what the model was actually told.
type recordedRequest struct {
	Kind requestKind
	Req  chatRequest
}

// provider is a test-owned OpenAI-compatible endpoint. It is deterministic:
// each request's reply is a pure function of the request and of the gates the
// test opens, and a request that is not part of the script is refused.
type provider struct {
	t   *testing.T
	srv *httptest.Server

	// childGate holds the child's first step until the test has observed the
	// waiting turn and steered the child, so the ordering the assertions need
	// is caused, not hoped for.
	childGate   chan struct{}
	releaseOnce sync.Once

	mu       sync.Mutex
	requests []recordedRequest
	refused  []string
	served   map[requestKind]int
}

func newProvider(t *testing.T) *provider {
	t.Helper()
	p := &provider{t: t, childGate: make(chan struct{}), served: make(map[requestKind]int)}
	p.srv = httptest.NewServer(http.HandlerFunc(p.serveHTTP))
	t.Cleanup(p.srv.Close)
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, refusal := range p.Refusals() {
			t.Logf("fixture provider refused: %s", refusal)
		}
	})
	return p
}

// URL is the provider's base URL. The child's config appends "/v1", because
// the client appends only the operation's path.
func (p *provider) URL() string { return p.srv.URL }

// ReleaseChild opens the child's gate: its first step answers from then on.
// It is idempotent, so a failing test can release the gate from a cleanup and
// never leave a request blocked.
func (p *provider) ReleaseChild() { p.releaseOnce.Do(func() { close(p.childGate) }) }

// Refusals reports every request the fixture could not serve.
func (p *provider) Refusals() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.refused...)
}

// Requests returns the requests served so far.
func (p *provider) Requests() []recordedRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]recordedRequest(nil), p.requests...)
}

// Served counts the requests answered per script kind.
func (p *provider) Served() map[requestKind]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[requestKind]int, len(p.served))
	for k, v := range p.served {
		out[k] = v
	}
	return out
}

// requestsOfKind returns the requests served for one part of the script.
func (p *provider) requestsOfKind(kind requestKind) []recordedRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []recordedRequest
	for _, r := range p.requests {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

// occurrences counts how many model requests carried a piece of text. It is how
// the test proves a steering instruction reached the model exactly once: the
// child's own request is the evidence that it was read, not merely accepted.
func (p *provider) occurrences(text string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, r := range p.requests {
		for _, m := range r.Req.Messages {
			n += strings.Count(m.text(), text)
		}
	}
	return n
}

// refusalsCount reports how many requests the fixture could not serve.
func (p *provider) refusalsCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.refused)
}

func (p *provider) serveHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		writeJSON(w, http.StatusOK, map[string]any{
			"object": "list",
			"data": []any{map[string]any{
				"id":       workerModel,
				"object":   "model",
				"owned_by": "factory-subagents-fixture",
			}},
		})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
		p.completion(w, r)
	default:
		p.refuse(w, r, "unexpected path")
	}
}

// refuse records the request and answers with an error, so an unscripted
// request can never look like a successful turn.
func (p *provider) refuse(w http.ResponseWriter, r *http.Request, reason string) {
	method, path := "", ""
	if r != nil {
		method, path = r.Method, r.URL.Path
	}
	p.mu.Lock()
	p.refused = append(p.refused, fmt.Sprintf("%s %s: %s", method, path, reason))
	p.mu.Unlock()
	if w == nil {
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{
		"error": map[string]any{"message": "factory subagents fixture: " + reason, "type": "fixture_error"},
	})
}

// toolCall is one native function call a reply emits.
type toolCall struct {
	ID   string
	Name string
	Args map[string]any
}

// reply is one scripted answer: either native tool calls, or the plain text
// the model writes as its final answer. The worker's provider advertises
// native tool calling, so the runtime sends a tool list and expects calls in
// the response — never an action envelope in the text.
type reply struct {
	text  string
	calls []toolCall
}

// factoryPrompt is the newest user message that carries a factory prompt: the
// implement or review prompt of a worker turn, or the continuation prompt that
// follows one in the same transcript.
func (r chatRequest) factoryPrompt() string {
	found := ""
	for _, m := range r.Messages {
		if m.Role != "user" {
			continue
		}
		text := m.text()
		if strings.Contains(text, "Factory operation ID:") {
			found = text
		}
	}
	return found
}

// delegateResults counts the results of an agent.run call in this request: how
// many delegations the turn has already attempted.
func (r chatRequest) delegateResults() int {
	n := 0
	for _, m := range r.Messages {
		text := m.text()
		if strings.HasPrefix(text, "Tool agent.run result:") || strings.HasPrefix(text, "Tool agent.run failed:") {
			n++
		}
	}
	return n
}

// classify decides which part of the script a request is, from the request
// itself. A delegated child is told apart by its own prompt: the runtime never
// offers it agent.run, and the test asserts that from the same text.
func classify(req chatRequest) (requestKind, bool) {
	if len(req.Messages) == 0 {
		return "", false
	}
	if strings.Contains(req.systemText(), "ad-hoc subtask delegated") {
		return kindChild, true
	}
	prompt := req.factoryPrompt()
	if prompt == "" {
		return "", false
	}
	switch {
	case strings.Contains(prompt, "Continuation ID:"):
		return kindContinuation, true
	case strings.Contains(prompt, "Review base SHA:"):
		return kindReview, true
	}
	switch req.delegateResults() {
	case 0:
		return kindImplement, true
	case 1:
		return kindSecondDelegate, true
	}
	return kindEarlyReport, true
}

var (
	operationIDPattern    = regexp.MustCompile(`Factory operation ID: ([^\s]+)`)
	continuationIDPattern = regexp.MustCompile(`Continuation ID: ([^\s]+)`)
	reviewBasePattern     = regexp.MustCompile(`Review base SHA: ([0-9a-f]+)`)
	reviewHeadPattern     = regexp.MustCompile(`Candidate head SHA: ([0-9a-f]+)`)
)

// implementationReport is the fresh report a continuation is asked for. It
// names the continuation it answers, which is what makes it this iteration's
// result rather than the turn before it.
func implementationReport(prompt string) (string, error) {
	op := operationIDPattern.FindStringSubmatch(prompt)
	cont := continuationIDPattern.FindStringSubmatch(prompt)
	if op == nil || cont == nil {
		return "", fmt.Errorf("continuation prompt names no operation or continuation")
	}
	report := map[string]any{
		"operationId":    op[1],
		"continuationId": cont[1],
		"outcome":        "complete",
		"summary":        "implemented the approved scope with the delegated analysis incorporated",
		"narration": map[string]any{
			"issue":     "the fixture issue",
			"approach":  "the fixture approach",
			"verification": "the fixture verification",
		},
	}
	data, err := json.Marshal(report)
	if err != nil {
		return "", err
	}
	return "```json\n" + string(data) + "\n```", nil
}

// reviewReport is the reviewer's report: it names the effect, the candidate it
// was shown and no findings, so a clean review is real evidence rather than a
// fabricated one.
func reviewReport(prompt string) (string, error) {
	op := operationIDPattern.FindStringSubmatch(prompt)
	base := reviewBasePattern.FindStringSubmatch(prompt)
	head := reviewHeadPattern.FindStringSubmatch(prompt)
	if op == nil || base == nil || head == nil {
		return "", fmt.Errorf("review prompt names no operation or candidate")
	}
	report := map[string]any{
		"operationId":      op[1],
		"outcome":          "complete",
		"summary":          "reviewed the candidate independently",
		"inputsAccessible": true,
		"baseSha":          base[1],
		"headSha":          head[1],
		"findings":         []any{},
	}
	data, err := json.Marshal(report)
	if err != nil {
		return "", err
	}
	return "```json\n" + string(data) + "\n```", nil
}

// completion takes the script's next step for this request.
func (p *provider) completion(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		p.refuse(w, r, "unreadable request body")
		return
	}
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		p.refuse(w, r, "malformed request body")
		return
	}
	kind, ok := classify(req)
	if !ok {
		p.refuse(w, r, "request is not part of the script: "+preview(req.lastUserText()))
		return
	}
	if kind == kindChild && !strings.Contains(req.systemText(), "ad-hoc subtask delegated") {
		p.refuse(w, r, "a request without the delegated-subtask prompt is not a child")
		return
	}
	p.mu.Lock()
	p.requests = append(p.requests, recordedRequest{Kind: kind, Req: req})
	p.served[kind]++
	p.mu.Unlock()

	rep, err := p.answer(kind, req)
	if err != nil {
		p.refuse(w, r, err.Error())
		return
	}
	if req.Stream {
		p.writeStream(w, rep)
		return
	}
	p.writeCompletion(w, rep)
}

// answer is the script itself.
func (p *provider) answer(kind requestKind, req chatRequest) (reply, error) {
	switch kind {
	case kindImplement:
		// The worker delegates to a child. Tool names cross the wire
		// sanitized (agent_run), because that is what an OpenAI-compatible
		// endpoint accepts and what the runtime maps back.
		return reply{calls: []toolCall{{ID: "call_delegate_1", Name: "agent_run", Args: map[string]any{
			"prompt":      "Audit the parser's handling of empty input and report what you find.",
			"description": "audit empty input handling",
		}}}}, nil
	case kindSecondDelegate:
		return reply{calls: []toolCall{{ID: "call_delegate_2", Name: "agent_run", Args: map[string]any{
			"prompt":      "A second delegation the approved concurrency bound must refuse.",
			"description": "second delegation",
		}}}}, nil
	case kindEarlyReport:
		// The final answer the turn writes while the child it started is
		// still running. It carries no factory report, so a controller that
		// accepted it would fail visibly.
		return reply{text: "Delegated the audit to a child; the work is still running."}, nil
	case kindChild:
		if req.delegateResults() > 0 || childHasToolResult(req) {
			// The child's second step. The steering instruction the user
			// pushed is in this request, so the child read it.
			return reply{text: "child report: the delegated audit is complete; the user's instruction was applied."}, nil
		}
		// The child's first step: held until the test has seen the waiting
		// turn and steered the child, then one read-only tool call so the
		// child takes another step and drains its steering queue.
		<-p.childGate
		return reply{calls: []toolCall{{ID: "call_child_1", Name: "file_read", Args: map[string]any{"path": "README.md"}}}}, nil
	case kindContinuation:
		text, err := implementationReport(req.factoryPrompt())
		return reply{text: text}, err
	case kindReview:
		text, err := reviewReport(req.factoryPrompt())
		return reply{text: text}, err
	}
	return reply{}, fmt.Errorf("unsupported script kind %q", kind)
}

// childHasToolResult reports whether a child has already taken a step: its
// tool results come back as user messages in the same text protocol.
func childHasToolResult(req chatRequest) bool {
	for _, m := range req.Messages {
		if strings.HasPrefix(m.text(), "Tool ") {
			return true
		}
	}
	return false
}

// writeStream answers with OpenAI-compatible SSE chunks: the text (or the tool
// calls, one chunk each) and a terminal finish_reason.
func (p *provider) writeStream(w http.ResponseWriter, rep reply) {
	var chunks []map[string]any
	finish := "stop"
	if len(rep.calls) > 0 {
		finish = "tool_calls"
		for i, call := range rep.calls {
			args, err := json.Marshal(call.Args)
			if err != nil {
				p.refuse(nil, nil, "marshal tool arguments")
				return
			}
			chunks = append(chunks, chunk(map[string]any{
				"role":    "assistant",
				"content": "",
				"tool_calls": []any{map[string]any{
					"index": i,
					"id":    call.ID,
					"type":  "function",
					"function": map[string]any{
						"name":      call.Name,
						"arguments": string(args),
					},
				}},
			}))
		}
	} else {
		chunks = append(chunks, chunk(map[string]any{"role": "assistant", "content": rep.text}))
	}
	last := chunks[len(chunks)-1]["choices"].([]any)[0].(map[string]any)
	last["finish_reason"] = finish

	var b strings.Builder
	for _, c := range chunks {
		data, err := json.Marshal(c)
		if err != nil {
			p.refuse(nil, nil, "marshal SSE chunk")
			return
		}
		b.WriteString("data: ")
		b.Write(data)
		b.WriteString("\n\n")
	}
	b.WriteString("data: [DONE]\n\n")

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
}

func chunk(delta map[string]any) map[string]any {
	return map[string]any{
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         delta,
			"finish_reason": nil,
		}},
	}
}

// writeCompletion answers a non-streaming request with the JSON equivalent.
func (p *provider) writeCompletion(w http.ResponseWriter, rep reply) {
	message := map[string]any{"role": "assistant", "content": rep.text}
	finish := "stop"
	if len(rep.calls) > 0 {
		finish = "tool_calls"
		message["content"] = ""
		calls := make([]any, 0, len(rep.calls))
		for _, call := range rep.calls {
			args, err := json.Marshal(call.Args)
			if err != nil {
				p.refuse(nil, nil, "marshal tool arguments")
				return
			}
			calls = append(calls, map[string]any{
				"id":   call.ID,
				"type": "function",
				"function": map[string]any{
					"name":      call.Name,
					"arguments": string(args),
				},
			})
		}
		message["tool_calls"] = calls
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      "chatcmpl-fixture",
		"object":  "chat.completion",
		"created": 0,
		"model":   workerModel,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finish,
		}},
	})
}

func preview(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "fixture: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

// ---------------------------------------------------------------------------
// A disposable Git repository, served over a real transport
// ---------------------------------------------------------------------------

// gitFixtures is one bare repository reachable over git's smart HTTP protocol
// on loopback. The factory records one URL for both git and the forge, so the
// URL has to be a hosted-looking one that git can also clone and push: a
// loopback smart-HTTP server is exactly that, and nothing leaves the machine.
type gitFixtures struct {
	t     *testing.T
	root  string
	bare  string
	url   string
	serve *httptest.Server
}

func newGitFixtures(t *testing.T) *gitFixtures {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not installed: %v", err)
	}
	root := t.TempDir()
	bare := filepath.Join(root, "o", "r.git")
	if err := os.MkdirAll(filepath.Dir(bare), 0o755); err != nil {
		t.Fatalf("create fixture root: %v", err)
	}
	git(t, root, "init", "--bare", bare)
	git(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")
	// The server is anonymous, so receive-pack must be allowed by the
	// repository itself rather than by an authenticated user.
	git(t, bare, "config", "http.receivepack", "true")

	seed := filepath.Join(t.TempDir(), "seed")
	git(t, root, "init", "-b", "main", seed)
	writeFile(t, filepath.Join(seed, "README.md"), "# fixture project\n")
	writeFile(t, filepath.Join(seed, "parser.go"), "package fixture\n\nfunc Parse(input string) (string, error) {\n\treturn input, nil\n}\n")
	// A Marshal session keeps its machine-local state out of Git by adding
	// one line to the project's .gitignore; an ordinary project already
	// carries it, so the fixture does too and a factory checkout of it stays
	// clean for the seed.
	writeFile(t, filepath.Join(seed, ".gitignore"), ".marshal/\n")
	git(t, seed, "add", "-A")
	git(t, seed, "commit", "-q", "-m", "fixture seed")
	git(t, seed, "remote", "add", "origin", bare)
	git(t, seed, "push", "-q", "origin", "main")

	f := &gitFixtures{t: t, root: root, bare: bare}
	f.serve = newSmartHTTPServer(t, root)
	f.url = f.serve.URL + "/o/r.git"
	return f
}

// head is the current commit of a branch in the bare repository: the one
// authority the fixture forge and the factory both read.
func (f *gitFixtures) head(branch string) string {
	f.t.Helper()
	return strings.TrimSpace(git(f.t, f.bare, "rev-parse", "refs/heads/"+branch))
}

// project makes the registered project the factory item belongs to: an
// ordinary clone of the same repository, with no .marshal/config.toml, so
// nothing about it is untrusted.
func (f *gitFixtures) project() string {
	f.t.Helper()
	dir := filepath.Join(f.t.TempDir(), "project")
	git(f.t, "", "clone", "-q", f.bare, dir)
	git(f.t, dir, "config", "user.email", "fixture@example.com")
	git(f.t, dir, "config", "user.name", "Fixture")
	return dir
}

// newSmartHTTPServer serves git http-backend over CGI, so a clone and a push
// over the recorded URL are real git operations against a real repository.
func newSmartHTTPServer(t *testing.T, projectRoot string) *httptest.Server {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git is not installed: %v", err)
	}
	handler := &cgi.Handler{
		Path: gitBin,
		Args: []string{"http-backend"},
		Dir:  projectRoot,
		Env: []string{
			"GIT_PROJECT_ROOT=" + projectRoot,
			"GIT_HTTP_EXPORT_ALL=1",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=/dev/null",
		},
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// ---------------------------------------------------------------------------
// The fixture forge
// ---------------------------------------------------------------------------

// forgeStub is a minimal GitHub-compatible API: the pull request the factory
// opens, reads back and (at the end) marks ready, plus the check runs that make
// a round's evidence real. It holds the same head authority the real forge
// would: whatever the branch is now.
type forgeStub struct {
	t  *testing.T
	mu sync.Mutex

	srv     *httptest.Server
	git     *gitFixtures
	branch  string
	base    string
	created bool
	draft   bool
	body    string
}

func newForgeStub(t *testing.T, git *gitFixtures, branch, base string) *forgeStub {
	t.Helper()
	f := &forgeStub{t: t, git: git, branch: branch, base: base}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *forgeStub) URL() string { return f.srv.URL }

// setBranch names the item branch the fixture forge reports as the pull
// request's head. The factory names it only once the item exists.
func (f *forgeStub) setBranch(branch string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.branch = branch
}

// Describe reports the pull request the fixture forge holds, so the test can
// assert what the factory published.
func (f *forgeStub) Describe() (created bool, draft bool, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.created, f.draft, f.body
}

// pr renders the pull request in the shape web/bridge's GitHub factory reads.
func (f *forgeStub) pr() map[string]any {
	return map[string]any{
		"number":   1,
		"html_url": "https://github.com/o/r/pull/1",
		"node_id":  "PR_node_1",
		"state":    "open",
		"draft":    f.draft,
		"head": map[string]any{
			"ref": f.branch,
			"sha": f.git.head(f.branch),
		},
		"base": map[string]any{
			"ref": f.base,
			"sha": f.git.head(f.base),
		},
	}
}

func (f *forgeStub) serveHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.HasSuffix(path, "/check-runs"):
		sha := shaInCheckRunsPath(path)
		writeJSON(w, http.StatusOK, map[string]any{
			"total_count": 1,
			"check_runs": []any{map[string]any{
				"id":         9,
				"name":       "build",
				"head_sha":   sha,
				"status":     "completed",
				"conclusion": "success",
			}},
		})
	case strings.HasSuffix(path, "/statuses"):
		writeJSON(w, http.StatusOK, []any{})
	case path == "/graphql":
		var q struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&q)
		f.draft = strings.Contains(q.Query, "convertPullRequestToDraft")
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"mutation": map[string]any{"pullRequest": map[string]any{
				"id": "PR_node_1", "isDraft": f.draft,
			}}},
		})
	case r.Method == http.MethodPatch && strings.Contains(path, "/pulls/"):
		var body struct {
			Body string `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.body = body.Body
		writeJSON(w, http.StatusOK, f.pr())
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/pulls"):
		var body struct {
			Draft bool   `json:"draft"`
			Head  string `json:"head"`
			Base  string `json:"base"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Draft {
			f.t.Error("factory opened a pull request that was not a draft")
		}
		f.created = true
		f.draft = true
		writeJSON(w, http.StatusCreated, f.pr())
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/pulls"):
		if !f.created {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
		writeJSON(w, http.StatusOK, []any{f.pr()})
	case r.Method == http.MethodGet && strings.Contains(path, "/pulls/"):
		if !f.created {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, f.pr())
	default:
		http.NotFound(w, r)
	}
}

// shaInCheckRunsPath reads the commit a check-run listing names.
func shaInCheckRunsPath(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, part := range parts {
		if part == "commits" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

// git runs one git command and fails the test when it fails.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.com",
		"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v: %s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
