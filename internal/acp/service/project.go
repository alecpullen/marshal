package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"marshal/internal/acpwire"
	"marshal/internal/sddauthor"
	"marshal/internal/sddplans"
	"marshal/internal/tools/native"
)

// maxReadFile caps what ReadFile returns. A file with more than this is cut,
// and reported as truncated.
const maxReadFile = 1 << 20

// FileIndex returns the repository's indexed file paths, for the @ completion
// popup. A session without an index returns none.
func (s *Session) FileIndex(ctx context.Context) ([]string, error) {
	if s.fileIndex == nil {
		return nil, nil
	}
	return s.fileIndex()
}

// Intelligence returns the doctor's intelligence checks: the index, the
// embeddings, the language servers and the index watcher.
func (s *Session) Intelligence(ctx context.Context) ([]acpwire.IntelligenceCheck, error) {
	return intelligenceChecks(s.state.ConfigSnapshot(), s.state.DB(), s.projectID), nil
}

// Plans lists the plans an SDD run can execute, from the configured plans
// directory. A plan that does not parse is listed with its error.
func (s *Session) Plans(ctx context.Context) ([]acpwire.PlanCandidate, error) {
	cfg := s.state.ConfigSnapshot()
	discovered := sddplans.Discover(s.state.WorkingDir, cfg.SDD.PlansDir)
	plans := make([]acpwire.PlanCandidate, 0, len(discovered))
	for _, c := range discovered {
		plan := acpwire.PlanCandidate{
			Path:      c.Path,
			Name:      c.Name,
			Slug:      c.Slug,
			Tasks:     c.Tasks,
			Done:      c.Done,
			Resumable: c.Resumable(),
		}
		if c.Err != nil {
			plan.Error = c.Err.Error()
		}
		if c.LedgerErr != nil {
			plan.LedgerError = c.LedgerErr.Error()
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

// DiscardCandidate removes one authored plan candidate. It refuses a path that
// is not a regular file directly inside the plans directory.
func (s *Session) DiscardCandidate(ctx context.Context, path string) error {
	cfg := s.state.ConfigSnapshot()
	return sddauthor.RemoveCandidate(path, s.state.WorkingDir, cfg.SDD.PlansDir)
}

// ScaffoldPlan writes the runnable starter plan into the plans directory. It
// never overwrites a file that is already there.
func (s *Session) ScaffoldPlan(ctx context.Context) (acpwire.ScaffoldResult, error) {
	cfg := s.state.ConfigSnapshot()
	path, existed, err := sddplans.Scaffold(s.state.WorkingDir, cfg.SDD.PlansDir)
	return acpwire.ScaffoldResult{Path: path, Existed: existed}, err
}

// errDiffRange is what Diff returns without both ends of its range.
var errDiffRange = errors.New("inproc: a diff needs a base and a head")

// Diff returns the unified diff of query's range in the session's working
// directory, with three lines of context. It never changes the repository.
func (s *Session) Diff(ctx context.Context, query acpwire.DiffQuery) (string, error) {
	if query.Base == "" || query.Head == "" {
		return "", errDiffRange
	}
	return s.git.Diff(s.state.WorkingDir, query.Base+".."+query.Head, 3)
}

// ReadFile reads one regular file, named relative to the session's working
// directory. It refuses a path that leaves the directory.
func (s *Session) ReadFile(ctx context.Context, path string) (acpwire.FileContent, error) {
	target, err := native.SafeResolve(s.state.WorkingDir, path)
	if err != nil {
		return acpwire.FileContent{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return acpwire.FileContent{}, err
	}
	if !info.Mode().IsRegular() {
		return acpwire.FileContent{}, fmt.Errorf("inproc: %s is not a regular file", path)
	}
	f, err := os.Open(target)
	if err != nil {
		return acpwire.FileContent{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxReadFile+1))
	if err != nil {
		return acpwire.FileContent{}, err
	}
	content := acpwire.FileContent{Path: path, Size: info.Size()}
	if len(data) > maxReadFile {
		content.Truncated = true
		data = data[:maxReadFile]
	}
	head := data
	if len(head) > 8192 {
		head = head[:8192]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		content.Binary = true
		return content, nil
	}
	content.Content = string(data)
	return content, nil
}
