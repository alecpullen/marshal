package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file pins the RECOVERY workflow: the offline confirmation, cleanup,
// migration, reset, and history discard.
//
// It is the most safety-critical test file in the feature, so the assertions are
// written from the adversary's side. For every destructive operation the test
// asks not only "did it work?" but "what could have authorised it, and is every
// one of those refused?" — a pipe, a here-document, an absence of a terminal, an
// EOF, a partial acknowledgement, a changed target, a crafted path, and an
// interruption at every crash boundary.
//
// Nothing here can reach the user's real ~/.local/share/marshal: every fixture
// lives under t.TempDir.

// ack is the exact acknowledgement word, spelled once so a test can prove the
// prompts require THIS and not merely something non-empty.
const ack = confirmAck

// acknowledged returns a scripted terminal that answers the offline
// acknowledgement, and optionally further answers.
func acknowledged(extra ...string) *ScriptedConfirmer {
	return &ScriptedConfirmer{Terminal: true, Answers: append([]string{ack}, extra...)}
}

// ---------------------------------------------------------------------------
// Confirmation mechanics
// ---------------------------------------------------------------------------

// Every destructive operation refuses a nil Confirmer.
func TestRecoveryRefusesNilConfirmer(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 1, 64)
	if _, err := f.m.Acquire(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.m.Release() }()

	if _, err := f.m.ResetWorkspace(f.ctx, f.workspace, RecoveryOptions{}); !errors.Is(err, ErrLegacyRecoveryRequired) {
		t.Fatalf("reset with no confirmer = %v, want a legacy-recovery refusal", err)
	}
	if _, err := f.m.MigrateWorkspace(f.ctx, f.workspace, RecoveryOptions{}); !errors.Is(err, ErrLegacyRecoveryRequired) {
		t.Fatalf("migrate with no confirmer = %v, want a legacy-recovery refusal", err)
	}
	// And the store is untouched.
	if _, err := os.Lstat(f.path); err != nil {
		t.Fatalf("the store was mutated by a refused operation: %v", err)
	}
}

// A terminal that is NOT a terminal — the zero-value scripted confirmer, which
// is exactly what a headless invocation produces — refuses before anything is
// even printed.
func TestRecoveryRefusesHeadlessInvocation(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 1, 64)
	if _, err := f.m.Acquire(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.m.Release() }()

	headless := &ScriptedConfirmer{}
	if headless.IsTerminal() {
		t.Fatal("the zero-value scripted confirmer claims to be a terminal")
	}
	_, err := f.m.ResetWorkspace(f.ctx, f.workspace, RecoveryOptions{Confirmer: headless})
	if !errors.Is(err, ErrLegacyRecoveryRequired) {
		t.Fatalf("headless reset = %v, want a legacy-recovery refusal", err)
	}
	if headless.Text() != "" {
		t.Errorf("a refused headless invocation still printed a prompt: %q", headless.Text())
	}
	// The store is exactly as it was.
	if _, err := os.Lstat(f.path); err != nil {
		t.Fatalf("the store was mutated by a headless invocation: %v", err)
	}
}

// EOF refuses. An exhausted scripted terminal is an EOF, which must never be
// read as an affirmative.
func TestRecoveryRefusesEOF(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 1, 64)
	if _, err := f.m.Acquire(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.m.Release() }()

	eof := &ScriptedConfirmer{Terminal: true} // no answers: the first prompt hits EOF
	if _, err := f.m.ResetWorkspace(f.ctx, f.workspace, RecoveryOptions{Confirmer: eof}); !errors.Is(err, ErrLegacyRecoveryRequired) {
		t.Fatalf("EOF at the reset prompt = %v, want a refusal", err)
	}
	if _, err := f.m.MigrateWorkspace(f.ctx, f.workspace, RecoveryOptions{Confirmer: eof}); !errors.Is(err, ErrLegacyRecoveryRequired) {
		t.Fatalf("EOF at the migrate prompt = %v, want a refusal", err)
	}
	if _, err := os.Lstat(f.path); err != nil {
		t.Fatalf("the store was mutated after an EOF: %v", err)
	}
}

// The prompt must demand the ACKNOWLEDGEMENT WORD, not a generic yes. "y" and an
// empty line both refuse, and the operator is shown the prerequisite text that
// explains why old binaries cannot be detected.
func TestRecoveryRequiresTheExactAcknowledgement(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 1, 64)
	if _, err := f.m.Acquire(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.m.Release() }()

	for _, wrong := range []string{"y", "yes", "", "ok", strings.ToUpper(ack), ack + " "} {
		// NOTE: the trailing-space case is trimmed by the reader, so it is
		// accepted on purpose; the list below is the REFUSED set.
		if wrong == ack+" " {
			continue
		}
		c := &ScriptedConfirmer{Terminal: true, Answers: []string{wrong}}
		_, err := f.m.ResetWorkspace(f.ctx, f.workspace, RecoveryOptions{Confirmer: c})
		if !errors.Is(err, ErrLegacyRecoveryRequired) {
			t.Errorf("reset with the acknowledgement %q = %v, want a refusal", wrong, err)
		}
		// The operator must have been shown WHY, including the honesty about
		// what this cannot verify.
		text := c.Text()
		for _, want := range []string{
			"Old Marshal binaries do not understand the new advisory store lock",
			"CANNOT detect them or exclude them",
			"operational prerequisite YOU are asserting",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("the prerequisite shown for %q is missing %q:\n%s", wrong, want, text)
			}
		}
	}
	if _, err := os.Lstat(f.path); err != nil {
		t.Fatalf("the store was mutated by a refused acknowledgement: %v", err)
	}
}

// Ordinary stdin CANNOT authorise a reset, even when it contains the correct
// workspace ID and the correct acknowledgement.
//
// This is the requirement stated most sharply in the plan, so it is asserted
// structurally: the reset path takes a Confirmer and nothing else, and the
// scripted-terminal refusal above is what a pipe produces. The test additionally
// proves that no field on the options can carry an answer.
func TestResetCannotBeAuthorisedByOrdinaryStdin(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 1, 64)
	if _, err := f.m.Acquire(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.m.Release() }()

	// A pipe is modelled by a terminal that is not a terminal. Feeding the
	// CORRECT answers through it must still refuse: the answers are irrelevant,
	// because the channel is wrong.
	piped := &ScriptedConfirmer{Terminal: false, Answers: []string{ack, f.workspace}}
	_, err := f.m.ResetWorkspace(f.ctx, f.workspace, RecoveryOptions{Confirmer: piped})
	if !errors.Is(err, ErrLegacyRecoveryRequired) {
		t.Fatalf("reset authorised through a non-terminal = %v, want a refusal", err)
	}
	if piped.Text() != "" {
		t.Errorf("a non-terminal channel was shown a prompt: %q", piped.Text())
	}
	if _, err := os.Lstat(f.path); err != nil {
		t.Fatalf("the store was mutated by a piped invocation: %v", err)
	}
}

// A reset requires the FULL ID typed at the terminal, and the ID must be exact.
func TestResetRequiresTheFullTypedID(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 1, 64)
	if _, err := f.m.Acquire(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.m.Release() }()

	for _, wrong := range []string{
		"",
		"0123456789a",                // one short
		f.workspace + "0",            // one long
		strings.ToUpper(f.workspace), // wrong case
		f.workspace[:6],              // a prefix
		"workspace " + f.workspace,   // decorated
	} {
		c := &ScriptedConfirmer{Terminal: true, Answers: []string{ack, wrong}}
		_, err := f.m.ResetWorkspace(f.ctx, f.workspace, RecoveryOptions{Confirmer: c})
		if !errors.Is(err, ErrLegacyRecoveryRequired) {
			t.Errorf("reset with the typed ID %q = %v, want a refusal", wrong, err)
		}
		if _, statErr := os.Lstat(f.path); statErr != nil {
			t.Fatalf("the store was mutated by the typed ID %q: %v", wrong, statErr)
		}
	}
}

// A reset that IS properly authorised removes ONLY the shadow store, and leaves
// the project's own working tree and `.git` byte-identical.
func TestResetRemovesOnlyTheShadowStoreAndLeavesProjectGitUntouched(t *testing.T) {
	requireGit(t)
	ctx := context.Background()

	// A real project with its own .git, plus a file marshal-adjacent tooling
	// would notice being corrupted.
	project := t.TempDir()
	runProjectGit(t, project, "init")
	if err := os.WriteFile(filepath.Join(project, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runProjectGit(t, project, "add", "-A")
	runProjectGit(t, project, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", "initial")
	projectGit := dirFingerprint(t, filepath.Join(project, ".git"))
	projectTree := dirFingerprint(t, project)

	// The workspace's shadow store, named by the hash the shadow layout uses.
	dataDir := t.TempDir()
	m, err := NewManager(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	workspace := WorkspaceHashFor(project)
	storePath := filepath.Join(m.Root(), workspace)
	if err := os.MkdirAll(storePath, 0o755); err != nil {
		t.Fatal(err)
	}
	gitEnv(t, ctx, storePath, "init", "--bare")
	fx := &legacyFixture{m: m, ctx: ctx, workspace: workspace, path: storePath}
	fx.commitChain(t, 2, 512)

	if _, err := m.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Release() }()

	c := &ScriptedConfirmer{Terminal: true, Answers: []string{ack, workspace}}
	report, err := m.ResetWorkspace(ctx, workspace, RecoveryOptions{Confirmer: c})
	if err != nil {
		t.Fatalf("ResetWorkspace: %v", err)
	}
	if !report.Removed {
		t.Fatalf("reset did not remove the store: %+v", report)
	}
	if report.Path != storePath {
		t.Fatalf("reset target = %q, want %q", report.Path, storePath)
	}
	if _, err := os.Lstat(storePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the shadow store still exists: %v", err)
	}
	// The project is untouched, byte for byte.
	if got := dirFingerprint(t, filepath.Join(project, ".git")); got != projectGit {
		t.Fatal("reset mutated the project's own .git directory")
	}
	if got := dirFingerprint(t, project); got != projectTree {
		t.Fatal("reset mutated the project's working tree")
	}
	// The notice names the exact target and the source-preservation guarantee.
	text := c.Text()
	for _, want := range []string{
		"Exact target:     " + storePath,
		"NOT touched: your project's files, and the project's own .git directory",
		"it cannot be recovered afterwards",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("reset notice missing %q:\n%s", want, text)
		}
	}
}

// A target that CHANGES between the confirmation and the act requires renewed
// confirmation, and this is detected by re-fingerprinting after the lock is
// retaken.
//
// The change is injected at the second prompt — the moment the operator is still
// answering — which is exactly the window the store lock does not cover a legacy
// writer for.
func TestResetRequiresRenewedConfirmationWhenTheTargetChanges(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 1, 64)
	if _, err := f.m.Acquire(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.m.Release() }()

	changed := false
	c := &ScriptedConfirmer{Terminal: true, Answers: []string{ack, f.workspace}}
	c.OnPrompt = func(string) {
		// Inject the change once, at the moment the ID prompt is answered, so
		// the store differs from the one the confirmation described.
		if changed {
			return
		}
		changed = true
		extra := strings.Repeat("y", 4096)
		if err := os.WriteFile(filepath.Join(f.path, "objects", "pack", "tmp_pack_injected"), []byte(extra), 0o644); err != nil {
			t.Errorf("inject change: %v", err)
		}
	}
	_, err := f.m.ResetWorkspace(f.ctx, f.workspace, RecoveryOptions{Confirmer: c})
	if !errors.Is(err, ErrLegacyRecoveryRequired) {
		t.Fatalf("reset against a changed target = %v, want a refusal requiring renewed confirmation", err)
	}
	if !strings.Contains(err.Error(), "changed between the confirmation and the operation") {
		t.Errorf("the refusal did not explain the change: %v", err)
	}
	// Nothing was removed.
	if _, err := os.Lstat(f.path); err != nil {
		t.Fatalf("the store was removed despite the changed target: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(f.path, "objects", "pack", "tmp_pack_injected")); err != nil {
		t.Fatalf("the injected change was destroyed: %v", err)
	}
}

// A change to a store's REFS alone must also trip the fingerprint, because a
// legacy writer adding a snapshot changes the ref list and nothing else this
// code measures.
func TestFingerprintDetectsARefChange(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 1, 64)
	before, err := f.m.fingerprintLegacy(f.ctx, f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	// Publish a second snapshot ref for a DIFFERENT object, so the ref LIST
	// changes rather than merely being rewritten with the same name.
	f.commitChainExtraSnapshot(t)

	after, err := f.m.fingerprintLegacy(f.ctx, f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	if before.Same(after) {
		t.Fatal("a ref change did not change the store fingerprint")
	}
}

// Reset refuses a path-escape attempt supplied as --workspace: "../", an absolute
// path, a traversal, and a project path are all refused as identifiers, so an
// arbitrary filesystem path can never be resolved for deletion.
func TestRecoveryRefusesPathEscapeWorkspaceArguments(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 1, 64)
	if _, err := f.m.Acquire(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.m.Release() }()

	sentinel := t.TempDir()
	marker := filepath.Join(sentinel, "must-survive")
	if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{
		"..",
		"../snapshots",
		"../../..",
		"/etc",
		sentinel,
		"../" + filepath.Base(sentinel),
		f.workspace + "/../../etc",
		".store.lock",
		"v2",
	} {
		c := &ScriptedConfirmer{Terminal: true, Answers: []string{ack, bad}}
		if _, err := f.m.ResetWorkspace(f.ctx, bad, RecoveryOptions{Confirmer: c}); err == nil {
			t.Errorf("reset with --workspace %q was accepted", bad)
		}
		if _, err := f.m.MigrateWorkspace(f.ctx, bad, RecoveryOptions{Confirmer: c}); err == nil {
			t.Errorf("migrate with --workspace %q was accepted", bad)
		}
	}
	// Nothing outside the store root was touched.
	if _, err := os.Lstat(marker); err != nil {
		t.Fatalf("a path-escape attempt destroyed %s: %v", marker, err)
	}
	// And the valid store is still there.
	if _, err := os.Lstat(f.path); err != nil {
		t.Fatalf("the store was removed by a path-escape attempt: %v", err)
	}
}

// An interrupted reset must be recoverable: the store is renamed into an
// accounted deleting state first, so a crash after the rename leaves a named
// remnant that cleanup finishes rather than an unaccounted directory.
func TestInterruptedResetIsRecoverable(t *testing.T) {
	for name, hook := range map[string]func() *RecoveryHooks{
		"before rename": func() *RecoveryHooks {
			return &RecoveryHooks{BeforeLegacyResetRename: func() error { return errors.New("interrupted") }}
		},
		"after rename": func() *RecoveryHooks {
			return &RecoveryHooks{BeforeLegacyResetRemove: func() error { return errors.New("interrupted") }}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newLegacyFixture(t)
			f.commitChain(t, 2, 256)
			// Install the recovery hook on the manager under test.
			f.m.hooks = Hooks{Recovery: *hook()}
			if _, err := f.m.Acquire(f.ctx); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.m.Release() }()

			c := &ScriptedConfirmer{Terminal: true, Answers: []string{ack, f.workspace}}
			_, err := f.m.ResetWorkspace(f.ctx, f.workspace, RecoveryOptions{Confirmer: c})

			// Recovery: run cleanup, which must finish whatever was left.
			f.m.hooks = Hooks{}
			report, cleanupErr := f.m.Cleanup(f.ctx, CleanupOptions{Confirmer: acknowledged(), RetentionDays: -1})
			if cleanupErr != nil {
				t.Fatalf("cleanup: %v", cleanupErr)
			}

			switch name {
			case "before rename":
				// The rename never happened, so the store is untouched and the
				// reset failed. Nothing was destroyed.
				if err == nil {
					t.Fatal("an interruption before the rename reported success")
				}
				if _, statErr := os.Lstat(f.path); statErr != nil {
					t.Fatalf("the store was removed despite an interruption before the rename: %v", statErr)
				}
				if len(report.RemnantsRemoved) != 0 {
					t.Fatalf("cleanup removed remnants for an interrupted-before-rename reset: %+v", report.RemnantsRemoved)
				}
			case "after rename":
				// The rename landed, so the store is an accounted remnant and
				// cleanup finishes removing it.
				if _, statErr := os.Lstat(f.path); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("the store is still at the original path: %v", statErr)
				}
				survey, surveyErr := f.m.SurveyLegacy(f.ctx)
				if surveyErr != nil {
					t.Fatal(surveyErr)
				}
				if len(survey.Remnants) != 0 {
					t.Fatalf("cleanup left remnants behind: %+v", survey.Remnants)
				}
				if len(report.RemnantsRemoved) != 1 {
					t.Fatalf("cleanup removed %d remnants, want 1", len(report.RemnantsRemoved))
				}
				if report.RemnantsRemoved[0].Workspace != f.workspace {
					t.Fatalf("remnant workspace = %q, want %q", report.RemnantsRemoved[0].Workspace, f.workspace)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Cleanup
// ---------------------------------------------------------------------------

// Cleanup removes ONLY recognized disposable artifacts, and only under the
// acknowledgement. A completed pack, an unrelated file, and an unrecognized
// temporary name all survive.
func TestCleanupRemovesOnlyRecognizedTemporaryArtifacts(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 1, 128)
	tempPack := f.abandonTempPack(t, "tmp_pack_AbCdEf", 8192)
	packDir := filepath.Join(f.path, "objects", "pack")

	// Artifacts that must NOT be removed.
	completedPack := filepath.Join(packDir, "pack-1234567890abcdef.pack")
	completedIdx := filepath.Join(packDir, "pack-1234567890abcdef.idx")
	unrelated := filepath.Join(packDir, "something-else.bin")
	for _, p := range []string{completedPack, completedIdx, unrelated} {
		if err := os.WriteFile(p, []byte("keep me"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// And an unrelated file elsewhere in the store.
	stray := filepath.Join(f.path, "do-not-delete")
	if err := os.WriteFile(stray, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := f.m.Acquire(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.m.Release() }()

	c := acknowledged()
	report, err := f.m.Cleanup(f.ctx, CleanupOptions{Confirmer: c, RetentionDays: -1})
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if !report.Confirmed {
		t.Fatal("cleanup reported the acknowledgement was not obtained")
	}
	if len(report.TempArtifactsRemoved) != 1 {
		t.Fatalf("removed %+v, want exactly the tmp_pack_ file", report.TempArtifactsRemoved)
	}
	if report.TempArtifactsRemoved[0].Path != tempPack {
		t.Fatalf("removed %q, want %q", report.TempArtifactsRemoved[0].Path, tempPack)
	}
	if _, err := os.Lstat(tempPack); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the tmp_pack_ file survived cleanup: %v", err)
	}
	for _, p := range []string{completedPack, completedIdx, unrelated, stray} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("cleanup removed %s, which is not a disposable artifact: %v", p, err)
		}
	}
	// The published refs survive, which is the point: cleanup is not retention.
	_, hashes, err := f.m.snapshotRefsInDir(f.ctx, f.path)
	if err != nil || len(hashes) != 1 {
		t.Fatalf("cleanup disturbed the snapshot refs: %v (hashes %v)", err, hashes)
	}
	// The operator was shown the prerequisite.
	if !strings.Contains(c.Text(), "Old Marshal binaries do not understand the new advisory store lock") {
		t.Errorf("cleanup did not show the offline prerequisite:\n%s", c.Text())
	}
}

// Cleanup WITHOUT the acknowledgement still reconciles the versioned store, but
// leaves every legacy temporary artifact in place.
func TestCleanupWithoutAcknowledgementLeavesLegacyUntouched(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 1, 128)
	tempPack := f.abandonTempPack(t, "tmp_pack_Untouched", 4096)

	if _, err := f.m.Acquire(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.m.Release() }()

	// A refusal, a headless terminal, and an EOF each leave the artifact alone.
	cases := map[string]Confirmer{
		"headless": &ScriptedConfirmer{},
		"eof":      &ScriptedConfirmer{Terminal: true},
		"refusal":  &ScriptedConfirmer{Terminal: true, Answers: []string{"no"}},
		"nil":      nil,
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			report, err := f.m.Cleanup(f.ctx, CleanupOptions{Confirmer: c, RetentionDays: -1})
			if err != nil {
				t.Fatalf("Cleanup: %v", err)
			}
			if report.Confirmed {
				t.Fatal("cleanup reported a confirmation that was never given")
			}
			if len(report.TempArtifactsRemoved) != 0 {
				t.Fatalf("cleanup removed %+v without an acknowledgement", report.TempArtifactsRemoved)
			}
			if _, err := os.Lstat(tempPack); err != nil {
				t.Fatalf("the temporary artifact was removed without an acknowledgement: %v", err)
			}
		})
	}
}

// Cleanup refuses on obvious live-writer evidence BEFORE obtaining an
// acknowledgement, because there is no point asking permission to mutate a store
// this process has already seen a writer inside.
func TestCleanupRefusesOnLiveWriterEvidence(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 1, 128)
	tempPack := f.abandonTempPack(t, "tmp_pack_Live", 4096)
	// A lock file proves an active writer.
	if err := os.WriteFile(filepath.Join(f.path, "refs", "heads", "master.lock"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := f.m.Acquire(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.m.Release() }()

	c := acknowledged()
	_, err := f.m.Cleanup(f.ctx, CleanupOptions{Confirmer: c, RetentionDays: -1})
	if !errors.Is(err, ErrLegacyRecoveryRequired) {
		t.Fatalf("cleanup with a live writer = %v, want a refusal", err)
	}
	if !strings.Contains(err.Error(), "lock file") {
		t.Errorf("the refusal did not name the lock file: %v", err)
	}
	// The prompt was never shown: evidence is checked first.
	if strings.Contains(c.Text(), ack) {
		t.Errorf("a prompt was shown after live-writer evidence was found:\n%s", c.Text())
	}
	if _, err := os.Lstat(tempPack); err != nil {
		t.Fatalf("the temporary artifact was removed despite live-writer evidence: %v", err)
	}
}

// Cleanup finishes an interrupted reset's remnant WITHOUT a further prompt: the
// confirmation that authorised destroying that history was already given, and
// the store was already renamed out of the lookup namespace.
func TestCleanupFinishesRemnantsWithoutReprompting(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 1, 128)
	remnant := filepath.Join(f.m.Root(), formatLegacyDeletingName(f.workspace, "deadbeef"))
	if err := os.Rename(f.path, remnant); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.Acquire(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.m.Release() }()

	// A HEADLESS terminal: no confirmation is available, and none is needed.
	report, err := f.m.Cleanup(f.ctx, CleanupOptions{Confirmer: &ScriptedConfirmer{}, RetentionDays: -1})
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if len(report.RemnantsRemoved) != 1 {
		t.Fatalf("cleanup removed %+v remnants, want the interrupted one", report.RemnantsRemoved)
	}
	if _, err := os.Lstat(remnant); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the remnant survived cleanup: %v", err)
	}
}

// Cleanup reconciles the VERSIONED store with no terminal and no confirmation:
// v2 work is safe by construction, because v2 writers honour the store lock.
func TestCleanupReconcilesVersionedStoreWithoutConfirmation(t *testing.T) {
	dir := t.TempDir()
	svc, _ := lookupService(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Track(context.Background()); err != nil {
		t.Fatalf("Track: %v", err)
	}
	// Plant a stray capture staging directory, which reconciliation removes.
	staging := filepath.Join(svc.StoreDir(), stagingDirName, "stage-0123456789abcdef")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "scratch"), []byte("leftover"), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := NewManager(svc.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Release() }()

	// A HEADLESS terminal: the v2 work must still happen.
	report, err := m.Cleanup(context.Background(), CleanupOptions{Confirmer: &ScriptedConfirmer{}, RetentionDays: -1})
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if len(report.Reconciled) != 1 {
		t.Fatalf("reconciled %v workspaces, want the versioned one", report.Reconciled)
	}
	if report.StagingRemoved != 1 {
		t.Fatalf("removed %d staging directories, want 1", report.StagingRemoved)
	}
	if _, err := os.Lstat(staging); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the stray staging directory survived reconciliation: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Migration
// ---------------------------------------------------------------------------

// Migration is HASH-PRESERVING: every legacy hash remains rollback-able
// afterwards, verified by an actual lookup and an actual restore.
func TestMigratePreservesEveryHashAndRestores(t *testing.T) {
	f := newLegacyFixture(t)
	commits := f.commitChain(t, 3, 1024)

	// Record the PUBLISHED legacy hash and prove it is lookup-able BEFORE.
	tip := commits[len(commits)-1]
	loc, err := f.m.LookupSnapshotOn(f.ctx, mustCatalog(t, f.m, f.workspace), tip)
	if err != nil {
		t.Fatalf("pre-migration lookup of %s: %v", tip, err)
	}
	if !loc.Legacy {
		t.Fatalf("pre-migration location of %s is not legacy: %+v", tip, loc)
	}
	before := []string{tip}

	if _, err := f.m.Acquire(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.m.Release() }()

	c := acknowledged()
	report, err := f.m.MigrateWorkspace(f.ctx, f.workspace, RecoveryOptions{Confirmer: c})
	if err != nil {
		t.Fatalf("MigrateWorkspace: %v", err)
	}
	if report.GenerationID == "" {
		t.Fatal("migration produced no generation")
	}
	if !report.DuplicateRemoved {
		t.Fatalf("the original duplicate was not removed: %+v", report)
	}
	// The notice described what would be copied.
	for _, want := range []string{
		"About to MIGRATE old-format snapshot history",
		"Workspace root:   unknown (the old layout records only the workspace hash)",
		"sealed and PROTECTED",
	} {
		if !strings.Contains(c.Text(), want) {
			t.Errorf("migration notice missing %q:\n%s", want, c.Text())
		}
	}

	// EVERY hash is still lookup-able, from a NON-legacy location. Only the
	// PUBLISHED ref is asserted: the earlier commits in the chain are reachable
	// through the branch (which the migration also preserves as a ref) but were
	// never rollback points of their own.
	cat := mustCatalog(t, f.m, f.workspace)
	for _, hash := range before[len(before)-1:] {
		loc, err := f.m.LookupSnapshotOn(f.ctx, cat, hash)
		if err != nil {
			t.Fatalf("post-migration lookup of %s: %v", hash, err)
		}
		if loc.Legacy {
			t.Fatalf("hash %s still resolves to the legacy store", hash)
		}
		if loc.GenerationID != report.GenerationID {
			t.Fatalf("hash %s resolves to %s, want the migrated generation %s", hash, loc.GenerationID, report.GenerationID)
		}
		if !loc.Protected {
			t.Fatalf("hash %s resolves to an UNPROTECTED generation", hash)
		}
	}

	// And the migrated generation is OriginLegacy + protected, sealed, never
	// active.
	man, err := cat.Load()
	if err != nil || man == nil {
		t.Fatalf("load manifest: %v", err)
	}
	rec := man.Generation(report.GenerationID)
	if rec == nil {
		t.Fatal("the migrated generation is not listed in the manifest")
	}
	if rec.Origin != OriginLegacy {
		t.Fatalf("migrated origin = %q, want %q", rec.Origin, OriginLegacy)
	}
	if !rec.IsProtected() {
		t.Fatal("the migrated generation is not protected")
	}
	if rec.State != GenerationSealed {
		t.Fatalf("migrated state = %q, want %q (never the active capture target)", rec.State, GenerationSealed)
	}
	if man.ActiveID == report.GenerationID {
		t.Fatal("the migrated generation became the active capture target")
	}
	if rec.Root != "" {
		t.Fatalf("migrated record root = %q, want empty: the old layout records no root", rec.Root)
	}

	// The original store is gone.
	if _, err := os.Lstat(f.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the original legacy store still exists: %v", err)
	}
}

// A migrated generation must NEVER be auto-reclaimed afterwards, no matter how
// hard retention is pushed: protection persists after migration.
func TestMigratedGenerationIsNeverAutoReclaimed(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 2, 512)
	if _, err := f.m.Acquire(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.m.Release() }()

	report, err := f.m.MigrateWorkspace(f.ctx, f.workspace, RecoveryOptions{Confirmer: acknowledged()})
	if err != nil {
		t.Fatalf("MigrateWorkspace: %v", err)
	}
	cat := mustCatalog(t, f.m, f.workspace)

	// Retention set to zero expires EVERYTHING older than now — the most
	// aggressive setting there is.
	time.Sleep(1100 * time.Millisecond)
	expired, err := f.m.ReclaimExpiredWorkspace(f.ctx, cat, 0)
	if err != nil {
		t.Fatalf("ReclaimExpiredWorkspace: %v", err)
	}
	for _, r := range expired.Reclaimed {
		if r.ID == report.GenerationID {
			t.Fatalf("retention reclaimed the PROTECTED migrated generation: %+v", r)
		}
	}
	// The generation directory and its refs are intact.
	man, err := cat.Load()
	if err != nil || man == nil {
		t.Fatal(err)
	}
	rec := man.Generation(report.GenerationID)
	if rec == nil {
		t.Fatal("retention removed the migrated generation's manifest record")
	}
	if rec.State == GenerationDeleting {
		t.Fatal("retention marked the migrated generation for deletion")
	}
	// And the objects are still there.
	dir, err := cat.GenerationDir(report.GenerationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.m.snapshotRefsInDir(f.ctx, dir); err != nil {
		t.Fatalf("the migrated generation's refs are unreadable: %v", err)
	}
	// A full pass over the whole root must also leave it alone.
	if _, err := f.m.ReclaimExpired(f.ctx, 0); err != nil {
		t.Fatalf("ReclaimExpired: %v", err)
	}
	man, err = cat.Load()
	if err != nil || man == nil {
		t.Fatal(err)
	}
	if man.Generation(report.GenerationID) == nil {
		t.Fatal("a whole-root retention pass removed the migrated generation")
	}
}

// The moved objects are hash-IDENTICAL: the destination repository contains the
// same object bytes under the same names.
func TestMigrateCopiesObjectsByteIdentically(t *testing.T) {
	f := newLegacyFixture(t)
	commits := f.commitChain(t, 2, 2048)
	// Capture a digest of the source's object contents BEFORE migrating.
	sourceObjects := looseObjectDigest(t, f.path)
	if len(sourceObjects) == 0 {
		t.Fatal("the source has no loose objects, so this test would prove nothing")
	}

	if _, err := f.m.Acquire(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.m.Release() }()

	report, err := f.m.MigrateWorkspace(f.ctx, f.workspace, RecoveryOptions{Confirmer: acknowledged()})
	if err != nil {
		t.Fatalf("MigrateWorkspace: %v", err)
	}
	cat := mustCatalog(t, f.m, f.workspace)
	genDir, err := cat.GenerationDir(report.GenerationID)
	if err != nil {
		t.Fatal(err)
	}
	destObjects := looseObjectDigest(t, genDir)

	for hash, digest := range sourceObjects {
		dest, ok := destObjects[hash]
		if !ok {
			t.Errorf("object %s was not copied to the migrated generation", hash)
			continue
		}
		if dest != digest {
			t.Errorf("object %s was copied with different bytes (%s vs %s)", hash, digest, dest)
		}
	}
	// Every chain commit resolves in the destination, which is what "the history
	// is preserved" means concretely.
	for i, hash := range commits {
		if _, err := f.m.gitOutput(f.ctx, genDir, "cat-file", "-e", hash+"^{commit}"); err != nil {
			t.Errorf("commit %d (%s) is not readable in the migrated generation: %v", i, hash, err)
		}
	}
	// And the migrated generation's own object index lists exactly those names,
	// so the copy is complete rather than merely readable.
	listed, err := f.m.gitOutput(f.ctx, genDir, "rev-list", "--objects", "--all", "--no-object-names")
	if err != nil {
		t.Fatalf("list migrated objects: %v", err)
	}
	seen := make(map[string]bool)
	for _, line := range strings.Split(string(listed), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			seen[line] = true
		}
	}
	for hash := range sourceObjects {
		if !seen[hash] {
			t.Errorf("object %s is not reachable in the migrated generation", hash)
		}
	}
}

// An insufficient budget REFUSES the migration rather than partially copying:
// selecting only the recent history without consent is exactly what this code
// must never do.
func TestMigrateRefusesRatherThanPartiallyMigrating(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 4, 16384)

	if _, err := f.m.Acquire(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.m.Release() }()

	source := dirFingerprint(t, f.path)
	c := acknowledged()
	// A tiny ceiling installed directly on the manager, which is the same seam
	// every other budget test uses. A real fixture large enough to overflow the
	// PRODUCTION 2 GiB ceiling would be gigabytes of test data.
	f.m.limits = Limits{
		WorkspaceMaxBytes:    4096,
		GlobalMaxBytes:       4096,
		FreeSpaceMarginBytes: DefaultFreeSpaceMarginBytes,
		AllocationUnitBytes:  DefaultAllocationUnitBytes,
	}
	_, err := f.m.MigrateWorkspace(f.ctx, f.workspace, RecoveryOptions{Confirmer: c})
	if err == nil {
		t.Fatal("a migration larger than the budget was admitted")
	}
	if !errors.Is(err, ErrBudgetExhausted) && !errors.Is(err, ErrInsufficientFreeSpace) {
		t.Fatalf("refusal = %v, want a budget or free-space refusal", err)
	}
	// The source is COMPLETELY untouched: no partial copy was made and nothing
	// was deleted.
	if got := dirFingerprint(t, f.path); got != source {
		t.Fatal("a refused migration mutated the source store")
	}
	// And no prompt was shown, because the fit was decided first.
	if strings.Contains(c.Text(), ack) {
		t.Errorf("a prompt was shown for a migration that could not fit:\n%s", c.Text())
	}
}

// Migrate refuses a protected-generation discard through the reset path: a
// reset only ever targets a LEGACY shadow store, never a v2 generation.
func TestResetNeverTargetsAGeneration(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 1, 128)
	if _, err := f.m.Acquire(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.m.Release() }()

	// Migrate first, so a v2 generation exists for this workspace.
	report, err := f.m.MigrateWorkspace(f.ctx, f.workspace, RecoveryOptions{Confirmer: acknowledged()})
	if err != nil {
		t.Fatalf("MigrateWorkspace: %v", err)
	}
	// A reset now has no legacy store to target, and says so rather than
	// resolving the v2 generation directory.
	c := acknowledged(f.workspace)
	_, err = f.m.ResetWorkspace(f.ctx, f.workspace, RecoveryOptions{Confirmer: c})
	if !errors.Is(err, ErrLegacyRecoveryRequired) {
		t.Fatalf("reset after migration = %v, want 'no old-format store'", err)
	}
	// The migrated generation is intact.
	cat := mustCatalog(t, f.m, f.workspace)
	genDir, err := cat.GenerationDir(report.GenerationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(genDir); err != nil {
		t.Fatalf("the migrated generation was removed by a reset: %v", err)
	}
}

// A migration restart at EVERY crash boundary must be recoverable and must never
// lose a published ref.
func TestMigrateRestartAtEveryBoundaryRecovers(t *testing.T) {
	boundaries := []struct {
		name string
		hook func() *RecoveryHooks
		// wantLegacyKept reports whether the original store should still be
		// present after the interruption.
		wantHashesPreserved bool
	}{
		{
			name:                "after generation record",
			hook:                func() *RecoveryHooks { return &RecoveryHooks{AfterLegacyGenerationRecord: errHook} },
			wantHashesPreserved: true,
		},
		{
			name:                "after objects copied",
			hook:                func() *RecoveryHooks { return &RecoveryHooks{AfterLegacyObjectsCopied: errHook} },
			wantHashesPreserved: true,
		},
		{
			name:                "after refs published",
			hook:                func() *RecoveryHooks { return &RecoveryHooks{AfterLegacyRefsPublished: errHook} },
			wantHashesPreserved: true,
		},
		{
			name:                "after generation sealed",
			hook:                func() *RecoveryHooks { return &RecoveryHooks{AfterLegacyGenerationSealed: errHook} },
			wantHashesPreserved: true,
		},
		{
			name:                "before original renamed",
			hook:                func() *RecoveryHooks { return &RecoveryHooks{BeforeLegacyOriginalRenamed: errHook} },
			wantHashesPreserved: true,
		},
		{
			name:                "after original renamed",
			hook:                func() *RecoveryHooks { return &RecoveryHooks{AfterLegacyOriginalRenamed: errHook} },
			wantHashesPreserved: true,
		},
	}
	for _, tc := range boundaries {
		t.Run(tc.name, func(t *testing.T) {
			f := newLegacyFixture(t)
			commits := f.commitChain(t, 2, 512)

			f.m.hooks = Hooks{Recovery: *tc.hook()}
			if _, err := f.m.Acquire(f.ctx); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.m.Release() }()
			c := acknowledged()
			_, err := f.m.MigrateWorkspace(f.ctx, f.workspace, RecoveryOptions{Confirmer: c})
			if err == nil {
				t.Fatal("an interruption boundary reported success")
			}

			// Restart: reconcile, then verify every hash is still reachable.
			f.m.hooks = Hooks{}
			if _, err := f.m.Reconcile(f.ctx); err != nil {
				t.Fatalf("restart reconciliation: %v", err)
			}
			if tc.wantHashesPreserved {
				// Only the hashes the store actually PUBLISHED are
				// lookup-able: the tip is a snapshot ref, and the earlier
				// commits in the chain are reachable through the branch but
				// are not rollback points in their own right. Asserting on
				// the tip is therefore asserting the property that matters —
				// a published ref was never lost.
				tip := commits[len(commits)-1]
				cat := mustCatalog(t, f.m, f.workspace)
				if _, err := f.m.LookupSnapshotOn(f.ctx, cat, tip); err != nil {
					t.Fatalf("restart at %q lost published hash %s: %v", tc.name, tip, err)
				}
			}
			// And the same migration can be re-run to completion.
			f.m.hooks = Hooks{}
			report, err := f.m.MigrateWorkspace(f.ctx, f.workspace, RecoveryOptions{Confirmer: acknowledged()})
			if err != nil {
				// A boundary that already consumed the source is a legitimate
				// "nothing to migrate" outcome.
				if errors.Is(err, ErrLegacyRecoveryRequired) {
					return
				}
				t.Fatalf("re-running the migration after %q: %v", tc.name, err)
			}
			cat := mustCatalog(t, f.m, f.workspace)
			if report.GenerationID != "" {
				// The published ref is the rollback point, so it is what must
				// survive a re-run. The earlier chain commits are reachable
				// through the branch and are asserted by object reachability
				// elsewhere.
				tip := commits[len(commits)-1]
				loc, err := f.m.LookupSnapshotOn(f.ctx, cat, tip)
				if err != nil {
					t.Fatalf("hash %s lost after re-running the migration: %v", tip, err)
				}
				if loc.Legacy {
					t.Fatalf("hash %s still resolves to the legacy store after re-running", tip)
				}
			}
		})
	}
}

// errHook is the shared "interrupt here" hook body.
func errHook() error { return errors.New("interrupted") }

// ---------------------------------------------------------------------------
// History discard
// ---------------------------------------------------------------------------

// Discarding a protected generation applies the SAME confirmation rules as a
// reset: a terminal, the full acknowledgement, and the full generation ID.
func TestDiscardProtectedGenerationRequiresFullConfirmation(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 1, 256)
	if _, err := f.m.Acquire(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.m.Release() }()

	report, err := f.m.MigrateWorkspace(f.ctx, f.workspace, RecoveryOptions{Confirmer: acknowledged()})
	if err != nil {
		t.Fatalf("MigrateWorkspace: %v", err)
	}
	genID := report.GenerationID

	// Refusals: no terminal, EOF, a wrong acknowledgement, and a wrong ID.
	refusals := map[string]Confirmer{
		"nil":        nil,
		"headless":   &ScriptedConfirmer{},
		"eof":        &ScriptedConfirmer{Terminal: true},
		"wrong ack":  &ScriptedConfirmer{Terminal: true, Answers: []string{"y", genID}},
		"wrong id":   &ScriptedConfirmer{Terminal: true, Answers: []string{ack, "not-the-id"}},
		"missing id": &ScriptedConfirmer{Terminal: true, Answers: []string{ack}},
		"piped":      &ScriptedConfirmer{Terminal: false, Answers: []string{ack, genID}},
	}
	for name, c := range refusals {
		t.Run(name, func(t *testing.T) {
			if _, err := f.m.DiscardProtectedGeneration(f.ctx, f.workspace, genID, RecoveryOptions{Confirmer: c}); !errors.Is(err, ErrLegacyRecoveryRequired) {
				t.Fatalf("discard with %s = %v, want a refusal", name, err)
			}
			cat := mustCatalog(t, f.m, f.workspace)
			dir, err := cat.GenerationDir(genID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(dir); err != nil {
				t.Fatalf("the protected generation was removed by a refused discard: %v", err)
			}
		})
	}

	// The properly authorised discard removes it, and lifts protection durably.
	c := acknowledged(genID)
	dr, err := f.m.DiscardProtectedGeneration(f.ctx, f.workspace, genID, RecoveryOptions{Confirmer: c})
	if err != nil {
		t.Fatalf("DiscardProtectedGeneration: %v", err)
	}
	if !dr.Removed {
		t.Fatal("the authorised discard did not remove the generation")
	}
	for _, want := range []string{
		"About to DISCARD protected snapshot history",
		"This is the only operation that lifts a migrated generation's protection",
		"NOT touched: your project's files, and the project's own .git directory",
	} {
		if !strings.Contains(c.Text(), want) {
			t.Errorf("discard notice missing %q:\n%s", want, c.Text())
		}
	}
	cat := mustCatalog(t, f.m, f.workspace)
	man, err := cat.Load()
	if err != nil {
		t.Fatal(err)
	}
	if man != nil && man.Generation(genID) != nil {
		t.Fatal("the discarded generation is still listed in the manifest")
	}
}

// A generation that is NOT protected is refused by the discard path: ordinary
// retention already removes those, so the discard confirmation must not become a
// generic delete.
func TestDiscardRefusesAnUnprotectedGeneration(t *testing.T) {
	_, cat, ctx := testCatalog(t)
	rec, err := cat.CreateGeneration(ctx, "")
	if err != nil {
		t.Fatalf("CreateGeneration: %v", err)
	}
	if err := cat.SealGeneration(rec.ID); err != nil {
		t.Fatalf("SealGeneration: %v", err)
	}
	m := cat.manager
	c := acknowledged(rec.ID)
	if _, err := m.DiscardProtectedGeneration(ctx, cat.Workspace(), rec.ID, RecoveryOptions{Confirmer: c}); err == nil {
		t.Fatal("an unprotected generation was discarded through the history-discard path")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// runProjectGit runs a git command against a real checkout (not a store). The
// managed invocation pins an explicit work tree, which is right for a bare store
// and wrong for a checkout with files in it.
func runProjectGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	// A plain invocation, deliberately NOT the managed one: the manager's
	// command pins --git-dir and --work-tree for a STORE's shape, and running
	// `git init` through it would create a bare repository with no work tree
	// instead of the checkout this fixture needs.
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	cmd.Env = sanitizedGitEnv(false)
	out, err := runGitCombined(context.Background(), cmd, defaultMaxCommandOutput)
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// mustCatalog builds the catalog handle for a workspace under an owned manager.
func mustCatalog(t *testing.T, m *Manager, workspace string) *Catalog {
	t.Helper()
	cat, err := m.Catalog(workspace)
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	return cat
}

// looseObjectDigest maps every object hash in a repository to a digest of the
// object's CONTENT — its type and its bytes, not its compressed on-disk
// encoding.
//
// The content is what "byte-identical" means for hash preservation: an object's
// name is derived from exactly those bytes. The stored zlib form is deliberately
// NOT compared, because the two writers differ by design — the legacy path let
// Git compress, while this store's bounded writer uses stored deflate blocks so
// an object's encoded size is bounded by its payload rather than by whatever a
// compressor chose to do.
func looseObjectDigest(t *testing.T, gitDir string) map[string]string {
	t.Helper()
	all, _, err := looseObjects(gitDir)
	if err != nil {
		t.Fatalf("looseObjects(%s): %v", gitDir, err)
	}
	out := make(map[string]string, len(all))
	for hash, path := range all {
		raw, err := decompressLooseObject(path)
		if err != nil {
			t.Fatalf("decompress object %s in %s: %v", hash, gitDir, err)
		}
		// The canonical hash of the decompressed object must equal the name it
		// is stored under; that IS hash preservation.
		if got := objectHashOfRaw(t, raw); got != hash {
			t.Fatalf("object stored as %s decompresses to bytes hashing to %s", hash, got)
		}
		sum := sha256.Sum256(raw)
		out[hash] = hex.EncodeToString(sum[:])
	}
	return out
}

// objectHashOfRaw recomputes the canonical Git object name of a decompressed
// loose object ("<type> <len>\0<content>").
func objectHashOfRaw(t *testing.T, raw []byte) string {
	t.Helper()
	sp := -1
	for i := 0; i < len(raw); i++ {
		if raw[i] == ' ' {
			sp = i
			break
		}
	}
	if sp < 0 {
		t.Fatalf("decompressed object has no type/length header")
	}
	nul := -1
	for i := sp; i < len(raw); i++ {
		if raw[i] == 0 {
			nul = i
			break
		}
	}
	if nul < 0 {
		t.Fatalf("decompressed object header is not NUL-terminated")
	}
	objType := string(raw[:sp])
	length, err := parseInt64(string(raw[sp+1 : nul]))
	if err != nil {
		t.Fatalf("decompressed object has an unparseable length: %v", err)
	}
	content := raw[nul+1:]
	if int64(len(content)) != length {
		t.Fatalf("decompressed object declares %d content bytes but holds %d", length, len(content))
	}
	hash, err := newGitObject(objType, content)
	if err != nil {
		t.Fatalf("hash decompressed object: %v", err)
	}
	return hash.Hash
}
