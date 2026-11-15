//go:build latencyprobe

package snapshot

// Investigation only. All captures use public source in an explicitly marked
// disposable checkout and a new private data directory. Timing, counting and
// profiling are separate passes; no production hooks or policies change.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strings"
	"testing"
	"time"
)

type traversalCount struct {
	Queries       int            `json:"allocated_size_queries"`
	Objects       int            `json:"object_queries"`
	UniqueObjects int            `json:"unique_objects"`
	Histogram     map[int]int    `json:"object_query_histogram"`
	ByPhase       map[string]int `json:"objects_by_call_path"`
	ByWorkspace   map[string]int `json:"objects_by_workspace"`
	Git           map[string]int `json:"git_commands"`
}

func traversalEmit(t *testing.T, kind, stage string, value any) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"kind": kind, "stage": stage, "value": value})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("TRAVERSAL_PROBE %s", b)
}

func traversalMeasure(t *testing.T, stage, phase string, sample int, fn func() error) {
	t.Helper()
	start := time.Now()
	if err := fn(); err != nil {
		t.Fatalf("%s/%s: %v", stage, phase, err)
	}
	traversalEmit(t, "timing", stage, map[string]any{"phase": phase, "sample": sample, "elapsed_ms": float64(time.Since(start).Nanoseconds()) / 1e6})
}

// This deliberately expensive counter pass is NOT used for timing. Queries
// count conservative allocation-accounting visits, not every stat/read syscall.
func traversalCountCapture(t *testing.T, s *Service, m *Manager, stage string) {
	t.Helper()
	c := traversalCount{Histogram: map[int]int{}, ByPhase: map[string]int{}, ByWorkspace: map[string]int{}, Git: map[string]int{}}
	paths := map[string]int{}
	originalSize, originalTree := m.allocatedSize, m.newTree
	m.allocatedSize = func(path string, info os.FileInfo) (int64, error) {
		c.Queries++
		parent := filepath.Base(filepath.Dir(path))
		if len(parent) == 2 && len(filepath.Base(path)) == 38 && validObjectHash(parent+filepath.Base(path)) {
			c.Objects++
			paths[path]++
			rel, _ := filepath.Rel(filepath.Join(m.root, v2DirName), path)
			c.ByWorkspace[strings.Split(filepath.ToSlash(rel), "/")[0]]++
			var pcs [48]uintptr
			n := runtime.Callers(2, pcs[:])
			frames := runtime.CallersFrames(pcs[:n])
			phase := "other"
			workspaceDetail := false
			for {
				f, more := frames.Next()
				switch {
				case strings.HasSuffix(f.Function, ".(*Manager).MeasureWorkspaceGenerations"):
					workspaceDetail = true
				case strings.HasSuffix(f.Function, ".(*Manager).usageWith"):
					phase = "usage.root"
					if workspaceDetail {
						phase = "usage.workspace_and_generation"
					}
				case strings.HasSuffix(f.Function, ".(*Manager).overRotationTarget"):
					phase = "post_publication_rotation"
				case strings.HasSuffix(f.Function, ".(*Manager).ReconcileWorkspace"):
					phase = "reconciliation"
				}
				if !more {
					break
				}
			}
			c.ByPhase[phase]++
		}
		return originalSize(path, info)
	}
	m.newTree = func(cmd *exec.Cmd) (*processTree, error) {
		for _, arg := range cmd.Args[1:] {
			switch arg {
			case "for-each-ref", "rev-list", "ls-files", "hash-object", "update-ref":
				c.Git[arg]++
			}
		}
		return newProcessTree(cmd)
	}
	defer func() { m.allocatedSize, m.newTree = originalSize, originalTree }()
	if hash, err := s.Track(context.Background()); err != nil || hash == "" {
		t.Fatalf("counted Track: %q %v", hash, err)
	}
	for _, count := range paths {
		c.Histogram[count]++
	}
	c.UniqueObjects = len(paths)
	if c.ByPhase["other"] != 0 || c.UniqueObjects < 2000 {
		t.Fatal("incomplete object accounting classification")
	}
	traversalEmit(t, "counts", stage, c)
}

func traversalComponents(t *testing.T, s *Service, m *Manager, cat *Catalog, stage string) {
	t.Helper()
	ctx := context.Background()
	if _, err := m.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := m.Release(); err != nil {
			t.Error(err)
		}
	}()
	req := CaptureRequest{WorkspaceRoot: s.workTree, Ignore: s.ignore, MaxFileBytes: s.maxFile, Bounds: defaultCaptureBounds()}
	man, err := cat.Load()
	if err != nil || man == nil || man.ActiveID == "" {
		t.Fatalf("no active manifest: %v", err)
	}
	gen, err := cat.GenerationDir(man.ActiveID)
	if err != nil {
		t.Fatal(err)
	}
	for sample := 0; sample < 3; sample++ {
		phases := []struct {
			name string
			run  func() error
		}{
			{"reconcile", func() error { _, err := m.ReconcileWorkspace(ctx, cat.Workspace()); return err }},
			{"select_and_plan", func() error {
				p, err := m.planCapture(ctx, cat, req)
				if err == nil && len(p.Entries) < 2000 {
					return fmt.Errorf("unexpected selected entries: %d", len(p.Entries))
				}
				return err
			}},
			{"global_usage", func() error { _, err := m.UsageContext(ctx); return err }},
			{"workspace_generations", func() error { _, err := m.MeasureWorkspaceGenerations(ctx, cat.Workspace()); return err }},
			{"reachability", func() error {
				n, err := m.UnreachableBytes(ctx, gen)
				if err == nil && n != 0 {
					return fmt.Errorf("unexpected unreachable bytes: %d", n)
				}
				return err
			}},
			{"admission", func() error {
				a, err := m.AdmitCapture(ctx, cat, req)
				if err == nil && a.Outcome != AdmissionAdmitted {
					return fmt.Errorf("unexpected admission outcome: %s", a.Outcome)
				}
				return err
			}},
			// One recursive measurement is a lower-bound comparison only: it
			// does not supply per-generation refs/protection or recovery facts.
			{"single_root_walk", func() error {
				info, err := os.Lstat(m.root)
				if err != nil {
					return err
				}
				u, err := m.measureTree(m.root, info, StoreKindWorkspace, "", m.limits.Normalize().AllocationUnitBytes)
				if err != nil {
					return err
				}
				global, err := m.UsageContext(ctx)
				if err == nil && u.Allocated != global.GlobalAllocated {
					return fmt.Errorf("one-walk total differs: %d != %d", u.Allocated, global.GlobalAllocated)
				}
				return err
			}},
		}
		for _, p := range phases {
			// The equality assertion above would include a second traversal in
			// its timing. Measure the raw walk separately after checking it.
			if p.name == "single_root_walk" {
				if err := p.run(); err != nil {
					t.Fatal(err)
				}
				p.run = func() error {
					info, err := os.Lstat(m.root)
					if err != nil {
						return err
					}
					_, err = m.measureTree(m.root, info, StoreKindWorkspace, "", m.limits.Normalize().AllocationUnitBytes)
					return err
				}
			}
			traversalMeasure(t, stage, p.name, sample, p.run)
		}
	}
}

func traversalCopySource(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == ".git" {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		if e.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !e.Type().IsRegular() {
			return fmt.Errorf("nonregular source %s", rel)
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		return errors.Join(copyErr, out.Close())
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotTraversalProbe(t *testing.T) {
	root := os.Getenv("MARSHAL_SNAPSHOT_PROBE_REPO")
	if root == "" {
		t.Skip("set MARSHAL_SNAPSHOT_PROBE_REPO to a marked disposable Marshal source checkout")
	}
	if !filepath.IsAbs(root) {
		t.Fatal("source root must be absolute")
	}
	marker, err := os.ReadFile(filepath.Join(root, ".git", "marshal-latency-fixture"))
	if err != nil || string(marker) != "disposable Marshal latency fixture\n" {
		t.Fatal("source is not an explicitly marked disposable fixture")
	}
	requireGit(t)
	dataDir := t.TempDir()
	s := New(dataDir, root, 2_000_000, []string{"node_modules/**", "vendor/**", "dist/**", ".git/**"}, nil)
	m, cat, err := s.store()
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(1_800_000_000, 0)
	m.now = func() time.Time { return clock }
	track := func() error {
		clock = clock.Add(time.Second)
		h, err := s.Track(context.Background())
		if err == nil && h == "" {
			return fmt.Errorf("empty published hash")
		}
		return err
	}
	traversalMeasure(t, "cold", "managed_capture", 0, track)
	for _, stage := range []string{"one_workspace", "four_workspaces"} {
		if stage == "four_workspaces" {
			for i := 1; i <= 3; i++ {
				otherRoot := filepath.Join(t.TempDir(), "repo")
				traversalCopySource(t, root, otherRoot)
				other := New(dataDir, otherRoot, s.maxFile, s.ignore, nil)
				if h, err := other.Track(context.Background()); err != nil || h == "" {
					t.Fatalf("seed unrelated store: %q %v", h, err)
				}
			}
		}
		for i := 0; i < 5; i++ {
			traversalMeasure(t, stage, "managed_capture", i, track)
		}
		traversalComponents(t, s, m, cat, stage)
		clock = clock.Add(time.Second)
		traversalCountCapture(t, s, m, stage)
		man, err := cat.Load()
		if err != nil || man == nil || man.Capture != nil {
			t.Fatalf("capture state not cleared: %v", err)
		}
		if _, err := m.Acquire(context.Background()); err != nil {
			t.Fatal(err)
		}
		usage, err := m.UsageContext(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		plan, err := m.planCapture(context.Background(), cat, CaptureRequest{WorkspaceRoot: s.workTree, Ignore: s.ignore, MaxFileBytes: s.maxFile})
		if releaseErr := m.Release(); releaseErr != nil {
			t.Fatal(releaseErr)
		}
		if err != nil {
			t.Fatal(err)
		}
		wantWorkspaces := 1
		if stage == "four_workspaces" {
			wantWorkspaces = 4
		}
		if len(usage.Generations) != wantWorkspaces {
			t.Fatalf("unexpected workspace count: %d", len(usage.Generations))
		}
		ids := make([]string, 0, len(usage.Generations))
		for id := range usage.Generations {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		traversalEmit(t, "inventory", stage, map[string]any{"workspace_ids": ids, "primary_workspace": cat.Workspace(), "allocated_bytes": usage.GlobalAllocated, "selected_entries": len(plan.Entries), "primary_refs": usage.Generations[cat.Workspace()].ByID[man.ActiveID].Refs})
	}
	if prefix := os.Getenv("MARSHAL_SNAPSHOT_PROBE_PROFILE_PREFIX"); prefix != "" {
		runtime.GC()
		initial, err := os.Create(prefix + ".initial.heap.pprof")
		if err != nil {
			t.Fatal(err)
		}
		if err := pprof.WriteHeapProfile(initial); err != nil {
			t.Fatal(err)
		}
		if err := initial.Close(); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(prefix + ".cpu.pprof")
		if err != nil {
			t.Fatal(err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			t.Fatal(err)
		}
		for range 10 {
			if err := track(); err != nil {
				pprof.StopCPUProfile()
				_ = f.Close()
				t.Fatal(err)
			}
		}
		pprof.StopCPUProfile()
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		f, err = os.Create(prefix + ".heap.pprof")
		if err != nil {
			t.Fatal(err)
		}
		runtime.GC()
		if err := pprof.WriteHeapProfile(f); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
