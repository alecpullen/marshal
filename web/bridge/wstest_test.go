package bridge

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

// wsSrc renders doc as the "source" a template stores. The fake control
// agent parses it by decoding it back, so tests need no TOML parser.
func wsSrc(t testing.TB, doc WSDoc) []byte {
	t.Helper()
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// wsParseAgent is a fake control agent whose workspace/parse decodes the
// source as JSON; a source beginning "BAD" parses with an error diagnostic.
func wsParseAgent() *fakeAgent {
	agent := newFakeAgent()
	agent.handler = func(method string, params json.RawMessage) (any, *rpcError, bool) {
		if method != "workspace/parse" {
			return nil, nil, false
		}
		var p struct {
			Source string `json:"source"`
		}
		_ = json.Unmarshal(params, &p)
		if strings.HasPrefix(p.Source, "BAD") {
			return map[string]any{"doc": WSDoc{}, "sections": []WSSection{}, "diagnostics": []WSDiag{{Line: 1, Message: "syntax error", Severity: "error"}}}, nil, true
		}
		var doc WSDoc
		if err := json.Unmarshal([]byte(p.Source), &doc); err != nil {
			return map[string]any{"doc": WSDoc{}, "sections": []WSSection{}, "diagnostics": []WSDiag{{Line: 1, Message: err.Error(), Severity: "error"}}}, nil, true
		}
		return map[string]any{"doc": doc, "sections": []WSSection{{Layer: 1, Key: "workspace", StartLine: 1, EndLine: 3}}, "diagnostics": []WSDiag{}}, nil, true
	}
	return agent
}

// wsFleet is a fleet with a fake parse agent and a fake image runtime.
func wsFleet(t *testing.T) (*Fleet, *fakeAgent, *fakeImages) {
	t.Helper()
	agent := wsParseAgent()
	f := ctlFleet(t, agent)
	imgs := newFakeImages()
	f.runner = imgs.run
	return f, agent, imgs
}

// fakeImages is a fake container runtime: it records every command and
// tracks which image tags exist.
type fakeImages struct {
	mu       sync.Mutex
	cmds     [][]string
	have     map[string]bool
	failTag  string // a build whose tag contains this fails
	buildGo  chan struct{}
	buildHit chan struct{}
	out      map[string]string // overrides output by args[0]
}

func newFakeImages() *fakeImages {
	return &fakeImages{have: map[string]bool{}, out: map[string]string{}}
}

func (r *fakeImages) run(name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	r.cmds = append(r.cmds, append([]string{name}, args...))
	r.mu.Unlock()
	if len(args) == 0 {
		return nil, nil
	}
	switch args[0] {
	case "build":
		if r.buildHit != nil {
			r.buildHit <- struct{}{}
			<-r.buildGo
		}
		tag := args[2]
		if r.failTag != "" && strings.Contains(tag, r.failTag) {
			return []byte("step failed: boom\n"), errors.New("exit status 1")
		}
		r.mu.Lock()
		r.have[tag] = true
		r.mu.Unlock()
		return []byte("Step 1/1 : " + tag + "\n"), nil
	case "image":
		if len(args) >= 3 && args[1] == "inspect" && args[2] == "--format" {
			return []byte("12345\n"), nil
		}
		r.mu.Lock()
		ok := r.have[args[len(args)-1]]
		r.mu.Unlock()
		if ok {
			return nil, nil
		}
		return nil, errors.New("no such image")
	case "inspect":
		return []byte("repo@sha256:aaaa\n"), nil
	}
	if o, ok := r.out[args[0]]; ok {
		return []byte(o), nil
	}
	return nil, nil
}

// count returns how many commands start with the given args prefix.
func (r *fakeImages) count(prefix ...string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.cmds {
		if len(c) >= 1+len(prefix) && strings.Join(c[1:1+len(prefix)], " ") == strings.Join(prefix, " ") {
			n++
		}
	}
	return n
}

// builtTags lists the tags passed to `build`, in order.
func (r *fakeImages) builtTags() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.cmds {
		if len(c) > 3 && c[1] == "build" {
			out = append(out, c[3])
		}
	}
	return out
}

func sampleDoc(name string) WSDoc {
	return WSDoc{
		Workspace: WSWorkspace{Name: name, Base: "debian:bookworm-slim", Toolchains: []string{"go@1.23.4"}},
		Packages:  WSPackages{Apt: []string{"git"}},
		Network:   WSNetwork{Mode: "allowlist", Egress: []string{"github.com"}},
		Resources: WSResources{CPU: 2, Memory: "4g", Timeout: "2h"},
	}
}
