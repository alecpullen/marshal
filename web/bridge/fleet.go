package bridge

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var ErrUnknownProject = errors.New("bridge: unknown project")

type projectRuntime struct {
	root     string
	child    *Child
	reg      *Registry
	log      *EventLog
	spawnErr error
}

type ProjectStatus struct {
	Root      string `json:"root"`
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
}

type Fleet struct {
	ws             *Workspace
	marshalBin     string
	fleetLog       *EventLog
	newChild       func(root string) *Child
	mu             sync.Mutex
	runtimes       map[string]*projectRuntime
	sessionProject map[string]string
}

func NewFleet(ws *Workspace, marshalBin string) *Fleet {
	f := &Fleet{ws: ws, marshalBin: marshalBin, fleetLog: NewEventLog(), runtimes: make(map[string]*projectRuntime), sessionProject: make(map[string]string)}
	f.newChild = func(string) *Child { return &Child{MarshalBin: marshalBin} }
	return f
}
func (f *Fleet) FleetLog() *EventLog { return f.fleetLog }

func (f *Fleet) runtimeFor(root string) (*projectRuntime, error) {
	f.mu.Lock()
	if rt, ok := f.runtimes[root]; ok {
		f.mu.Unlock()
		if rt.spawnErr != nil {
			return nil, rt.spawnErr
		}
		return rt, nil
	}
	f.mu.Unlock()

	child := f.newChild(root)
	reg := NewRegistry(child)
	reg.RootCwd = root
	log := NewEventLog()
	Attach(log, child, reg)
	rt := &projectRuntime{root: root, child: child, reg: reg, log: log}
	if err := child.Start(); err != nil {
		rt.spawnErr = fmt.Errorf("start marshal acp for %s: %w (stderr: %s)", root, err, child.StderrLog())
		f.mu.Lock()
		f.runtimes[root] = rt
		f.mu.Unlock()
		return nil, rt.spawnErr
	}
	f.mu.Lock()
	if existing, ok := f.runtimes[root]; ok {
		f.mu.Unlock()
		child.Stop()
		if existing.spawnErr != nil {
			return nil, existing.spawnErr
		}
		return existing, nil
	}
	f.runtimes[root] = rt
	f.mu.Unlock()
	return rt, nil
}

func (f *Fleet) liveRuntimeForSession(id string) (*projectRuntime, error) {
	f.mu.Lock()
	root, ok := f.sessionProject[id]
	rt := f.runtimes[root]
	f.mu.Unlock()
	if !ok || rt == nil {
		return nil, ErrUnknownSession
	}
	if rt.spawnErr != nil {
		return nil, rt.spawnErr
	}
	return rt, nil
}

func (f *Fleet) RuntimeForSession(id string) (*projectRuntime, error) {
	if rt, err := f.liveRuntimeForSession(id); err == nil {
		return rt, nil
	} else if !errors.Is(err, ErrUnknownSession) {
		return nil, err
	}
	a, ok := f.ws.Agent(id)
	if !ok || a.Project == "" {
		return nil, ErrUnknownSession
	}
	rt, err := f.runtimeFor(a.Project)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := rt.reg.Load(ctx, a.Project, id); err != nil {
		return nil, fmt.Errorf("restore agent %s: %w", id, err)
	}
	f.track(id, a.Project)
	return rt, nil
}
func (f *Fleet) LogForSession(id string) (*EventLog, error) {
	rt, err := f.RuntimeForSession(id)
	if err != nil {
		return nil, err
	}
	return rt.log, nil
}
func (f *Fleet) RegistryForSession(id string) (*Registry, error) {
	rt, err := f.RuntimeForSession(id)
	if err != nil {
		return nil, err
	}
	return rt.reg, nil
}
func (f *Fleet) track(id, root string) { f.mu.Lock(); f.sessionProject[id] = root; f.mu.Unlock() }

func (f *Fleet) Spawn(ctx context.Context, root, name, mode string) (string, error) {
	if err := f.ws.AddProject(root); err != nil {
		return "", err
	}
	rt, err := f.runtimeFor(root)
	if err != nil {
		return "", err
	}
	id, err := rt.reg.New(ctx, root, "")
	if err != nil {
		return "", err
	}
	f.track(id, root)
	if mode != "" {
		if err := rt.reg.SetMode(ctx, id, mode); err != nil {
			return id, fmt.Errorf("agent created but setting mode %q failed: %w", mode, err)
		}
	}
	if err := f.ws.PutAgent(Agent{ID: id, Project: root, Name: name, Mode: mode, CreatedAt: time.Now().UTC()}); err != nil {
		return id, fmt.Errorf("agent created but recording it failed: %w", err)
	}
	return id, nil
}

func (f *Fleet) ProjectStatus() []ProjectStatus {
	roots := f.ws.Projects()
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := make(map[string]bool)
	out := make([]ProjectStatus, 0, len(roots)+len(f.runtimes))
	add := func(root string) {
		if seen[root] {
			return
		}
		seen[root] = true
		st := ProjectStatus{Root: root, Available: true}
		if rt := f.runtimes[root]; rt != nil && rt.spawnErr != nil {
			st.Available = false
			st.Error = rt.spawnErr.Error()
		}
		out = append(out, st)
	}
	for _, root := range roots {
		add(root)
	}
	for root := range f.runtimes {
		add(root)
	}
	return out
}

func (f *Fleet) Close() {
	f.mu.Lock()
	rts := make([]*projectRuntime, 0, len(f.runtimes))
	for _, rt := range f.runtimes {
		rts = append(rts, rt)
	}
	f.runtimes = make(map[string]*projectRuntime)
	f.sessionProject = make(map[string]string)
	f.mu.Unlock()
	for _, rt := range rts {
		if rt.spawnErr == nil {
			rt.child.Stop()
		}
	}
}
