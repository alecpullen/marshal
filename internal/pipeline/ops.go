// internal/pipeline/ops.go
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"marshal/internal/tools/patch"
)


// applyPatchOp applies a SEARCH/REPLACE patch to a file in dir. It uses the
// existing patch engine's ValidatePatch to check uniqueness, then ApplyPatch
// to perform the replacement.
func applyPatchOp(dir string, op *PatchOp) error {
	path := filepath.Join(dir, op.File)
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("ops patch: read %s: %w", op.File, err)
	}
	fp := patch.FilePatch{
		Path: op.File,
		Chunks: []patch.PatchChunk{
			{Search: op.Search, Replace: op.Replace},
		},
	}
	ok, err := patch.ValidatePatch(string(content), fp)
	if err != nil {
		return fmt.Errorf("ops patch: validate %s: %w", op.File, err)
	}
	if !ok {
		return fmt.Errorf("ops patch: validation failed for %s", op.File)
	}
	newContent := patch.ApplyPatch(string(content), fp)
	if err := os.WriteFile(path, []byte(newContent), 0o644); err != nil {
		return fmt.Errorf("ops patch: write %s: %w", op.File, err)
	}
	return nil
}

// applyFileOp writes a complete new file. It refuses to overwrite an existing
// file unless Replace is true.
func applyFileOp(dir string, op *FileOp) error {
	path := filepath.Join(dir, op.Path)
	if !op.Replace {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("ops file: %s already exists and replace is not set", op.Path)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("ops file: mkdir for %s: %w", op.Path, err)
	}
	if err := os.WriteFile(path, []byte(op.Content), 0o644); err != nil {
		return fmt.Errorf("ops file: write %s: %w", op.Path, err)
	}
	return nil
}

// runRunOp executes a command and checks its exit status. An expected
// non-zero exit code (ExpectExit) is treated as success.
func runRunOp(ctx context.Context, dir string, runner CommandRunner, op *RunOp) error {
	out, err := runner.Run(ctx, dir, op.Command)
	if err != nil {
		var exitErr interface{ ExitCode() int }
		if errors.As(err, &exitErr) && exitErr.ExitCode() == op.ExpectExit {
			return nil
		}
		return fmt.Errorf("ops run: %v: %w\noutput: %s", op.Command, err, out)
	}
	if op.ExpectExit != 0 {
		return fmt.Errorf("ops run: %v: expected exit %d but command succeeded", op.Command, op.ExpectExit)
	}
	return nil
}

// runVerifyOps executes all verify-phase RunOps and returns the first
// failure as a VerifyResult. It is called inside the verify gate loop so
// that a failing verify command triggers the fixer rounds.
func runVerifyOps(ctx context.Context, dir string, runner CommandRunner, ops []Operation) VerifyResult {
	for _, op := range ops {
		r, ok := op.(*RunOp)
		if !ok || r.Status != OpExecutable || r.Phase != "verify" {
			continue
		}
		if err := runRunOp(ctx, dir, runner, r); err != nil {
			return VerifyResult{
				OK:            false,
				FailedCommand: strings.Join(r.Command, " "),
				Output:        err.Error(),
			}
		}
	}
	return VerifyResult{OK: true}
}

// checkAssert evaluates one assertion against the worktree state.
func checkAssert(ctx context.Context, dir string, runner CommandRunner, op *AssertOp) error {
	switch op.Kind {
	case AssertFileExists:
		path := filepath.Join(dir, op.File)
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("assert file.exists: %s: %w", op.File, err)
		}
		return nil

	case AssertFileMatches:
		path := filepath.Join(dir, op.File)
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("assert file.matches: read %s: %w", op.File, err)
		}
		if !strings.Contains(string(content), op.Content) {
			return fmt.Errorf("assert file.matches: %s does not contain %q", op.File, op.Content)
		}
		return nil

	case AssertTestPasses:
		out, err := runner.Run(ctx, dir, op.Command)
		if err != nil {
			return fmt.Errorf("assert test.passes: %v: %w\noutput: %s", op.Command, err, out)
		}
		return nil

	case AssertGoSymbol:
		// Go symbol checks require tree-sitter or gopls. For the first
		// version, fall back to a text search: the symbol string must
		// appear in the file. This is intentionally conservative.
		path := filepath.Join(dir, op.File)
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("assert go.symbol: read %s: %w", op.File, err)
		}
		parts := strings.SplitN(op.Symbol, ".", 2)
		if len(parts) < 2 {
			return fmt.Errorf("assert go.symbol: malformed symbol %q", op.Symbol)
		}
		// Search for the struct/type name and the field/method name.
		if !strings.Contains(string(content), parts[0]) {
			return fmt.Errorf("assert go.symbol: %s not found in %s", parts[0], op.File)
		}
		if !strings.Contains(string(content), parts[1]) {
			return fmt.Errorf("assert go.symbol: %s.%s not found in %s", parts[0], parts[1], op.File)
		}
		return nil

	case AssertGoCompiles:
		out, err := runner.Run(ctx, dir, []string{"go", "build", op.Package})
		if err != nil {
			return fmt.Errorf("assert go.compiles: %s: %w\noutput: %s", op.Package, err, out)
		}
		return nil

	default:
		return fmt.Errorf("assert: unknown kind %q", op.Kind)
	}
}

// preflightOps checks preconditions for all operations against the worktree
// state and sets each operation's status. It returns diagnostics for any
// operations that cannot be satisfied.
func preflightOps(dir string, ops []Operation) []Diagnostic {
	var diags []Diagnostic
	for _, op := range ops {
		switch o := op.(type) {
		case *PatchOp:
			path := filepath.Join(dir, o.File)
			content, err := os.ReadFile(path)
			if err != nil {
				o.Status = OpBlocked
				diags = append(diags, Diagnostic{
					Severity: DiagError,
					Message:  fmt.Sprintf("patch: file %s not found", o.File),
				})
				continue
			}
			count := strings.Count(string(content), o.Search)
			if count == 0 {
				o.Status = OpBlocked
				diags = append(diags, Diagnostic{
					Severity: DiagError,
					Message:  fmt.Sprintf("patch: search block not found in %s", o.File),
				})
				continue
			}
			if count > 1 {
				o.Status = OpBlocked
				diags = append(diags, Diagnostic{
					Severity: DiagError,
					Message:  fmt.Sprintf("patch: ambiguous match (%d locations) in %s", count, o.File),
				})
				continue
			}
			o.Status = OpExecutable

		case *FileOp:
			path := filepath.Join(dir, o.Path)
			if !o.Replace {
				if _, err := os.Stat(path); err == nil {
					o.Status = OpBlocked
					diags = append(diags, Diagnostic{
						Severity: DiagError,
						Message:  fmt.Sprintf("file: %s already exists and replace is not set", o.Path),
					})
					continue
				}
			}
			o.Status = OpExecutable

		case *RunOp:
			o.Status = OpExecutable

		case *AssertOp:
			o.Status = OpExecutable

		case *AgentOp:
			o.Status = OpAgent
		}
	}
	return diags
}
