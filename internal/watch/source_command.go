package watch

import (
	"context"
	"fmt"
)

// sampleCommand runs the watch's command through the injected RunSample seam
// and returns a Sample carrying the tail-capped stdout and the exit code.
// A non-zero exit code is data, not an error: it flows into Sample.ExitCode so
// exit_code conditions can match it. Only err != nil (spawn/sandbox failure)
// counts as an iteration error against the error budget.
func (s sourceSampler) sampleCommand(ctx context.Context, w *watch) (Sample, error) {
	if s.deps.RunSample == nil {
		return Sample{}, fmt.Errorf("command watch %q: runSample not configured", w.name)
	}
	dir := ""
	if s.deps.DirFn != nil {
		dir = s.deps.DirFn()
	}
	stdout, exitCode, err := s.deps.RunSample(ctx, w.command, dir)
	if err != nil {
		return Sample{}, err
	}
	return Sample{Stdout: stdout, ExitCode: exitCode}, nil
}
