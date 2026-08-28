package bridge

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestDerivedDockerfileCopiesMarshalIn(t *testing.T) {
	got := derivedDockerfile("node:20", "ghcr.io/marshal/agent:v1.2.3")
	if !strings.Contains(got, "FROM node:20") {
		t.Errorf("base missing:\n%s", got)
	}
	if !strings.Contains(got, "COPY --from=ghcr.io/marshal/agent:v1.2.3") {
		t.Errorf("marshal is not copied in:\n%s", got)
	}
	if !strings.Contains(got, "/usr/local/bin/marshal") {
		t.Errorf("marshal lands somewhere unexpected:\n%s", got)
	}
}

// TestDerivedTagKeysOnDigestNotTag is the important one: keying on the
// base TAG would silently serve a stale derivation after an upstream
// push, running an agent against a base you think you updated.
func TestDerivedTagKeysOnDigestNotTag(t *testing.T) {
	a := derivedTag("sha256:aaaa", "v1.2.3")
	b := derivedTag("sha256:bbbb", "v1.2.3")
	if a == b {
		t.Fatal("two different base digests produced the same derived tag")
	}
	c := derivedTag("sha256:aaaa", "v1.3.0")
	if a == c {
		t.Fatal("a marshal upgrade did not invalidate the derived image")
	}
	if derivedTag("sha256:aaaa", "v1.2.3") != a {
		t.Fatal("derivedTag is not deterministic")
	}
}

func TestEnsureDerivedImageReusesTheCache(t *testing.T) {
	var builds int
	var inspects int
	f := testFleetWithRunner(t, func(name string, args ...string) ([]byte, error) {
		switch {
		case len(args) > 0 && args[0] == "build":
			builds++
			return nil, nil
		case len(args) > 0 && args[0] == "inspect":
			return []byte("sha256:aaaa\n"), nil
		case len(args) > 0 && args[0] == "image":
			// First call: image absent (inspect errors). Subsequent calls:
			// image exists after the build.
			inspects++
			if inspects == 1 {
				return nil, errors.New("no such image")
			}
			return nil, nil
		}
		return nil, nil
	})

	for i := 0; i < 3; i++ {
		if _, err := f.ensureDerivedImage(context.Background(), "node:20"); err != nil {
			t.Fatalf("ensureDerivedImage: %v", err)
		}
	}
	if builds != 1 {
		t.Fatalf("built %d times; expected exactly one build then cache reuse", builds)
	}
}

func TestBuildFailureNeverFallsBackToTheDefault(t *testing.T) {
	f := testFleetWithRunner(t, func(name string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "build" {
			return []byte("no builder available"), errors.New("build failed")
		}
		if len(args) > 0 && args[0] == "image" {
			return nil, errors.New("no such image") // absent, so we build
		}
		return []byte("sha256:aaaa\n"), nil
	})

	img, err := f.ensureDerivedImage(context.Background(), "node:20")
	if err == nil {
		t.Fatal("a failed derive returned success")
	}
	if img != "" {
		t.Fatalf("a failed derive returned an image (%q); running an agent in the wrong environment is worse than refusing", img)
	}
	if !strings.Contains(err.Error(), "node:20") {
		t.Errorf("the error does not name the declared image: %v", err)
	}
}

// testFleetWithRunner builds a Fleet whose container-runtime commands are
// faked by runner, so the derive path is exercisable without a daemon.
func testFleetWithRunner(t *testing.T, runner commandRunner) *Fleet {
	t.Helper()
	ws := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatal(err)
	}
	f := NewFleet(ws, "unused", nil, "", Limits{}, "", nil)
	f.runner = runner
	t.Cleanup(f.Close)
	return f
}
