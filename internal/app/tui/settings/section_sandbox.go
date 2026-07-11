package settings

import (
	"strconv"

	"charm.land/huh/v2"
)

type sandboxBuffers struct {
	memory, cpu, procs, fileSize string
}

func newSandboxPane(s *state) sectionPane {
	b := &sandboxBuffers{}
	form := newScalarPane(func() *huh.Form {
		b.memory = strconv.Itoa(s.cfg.Tools.Shell.Sandbox.MemoryLimitMB)
		b.cpu = strconv.Itoa(s.cfg.Tools.Shell.Sandbox.CPUSeconds)
		b.procs = strconv.Itoa(s.cfg.Tools.Shell.Sandbox.MaxProcesses)
		b.fileSize = strconv.Itoa(s.cfg.Tools.Shell.Sandbox.FileSizeLimitMB)
		return newSectionForm(
			huh.NewSelect[string]().Key("Backend").Title("Backend").
				Options(
					huh.NewOption("restricted", "restricted"),
					huh.NewOption("container", "container"),
					huh.NewOption("passthrough", "passthrough"),
				).
				Value(&s.cfg.Tools.Shell.Sandbox.Backend),
			numField("Memory limit (MB)", &b.memory, 0, func(v int) { s.cfg.Tools.Shell.Sandbox.MemoryLimitMB = v }),
			numField("CPU seconds", &b.cpu, 0, func(v int) { s.cfg.Tools.Shell.Sandbox.CPUSeconds = v }),
			numField("Max processes", &b.procs, 0, func(v int) { s.cfg.Tools.Shell.Sandbox.MaxProcesses = v }),
			numField("File size limit (MB)", &b.fileSize, 0, func(v int) { s.cfg.Tools.Shell.Sandbox.FileSizeLimitMB = v }),
			huh.NewInput().Key("Container runtime").Title("Container runtime").Value(&s.cfg.Tools.Shell.Sandbox.ContainerRuntime),
			huh.NewInput().Key("Container image").Title("Container image").Value(&s.cfg.Tools.Shell.Sandbox.ContainerImage),
			huh.NewConfirm().Key("Allow fallback").Title("Allow fallback").Value(&s.cfg.Tools.Shell.Sandbox.AllowFallback),
		)
	})
	return newMixedPane(form,
		newListStrings("Env allowlist", &s.cfg.Tools.Shell.Sandbox.EnvAllowlist),
		newListStrings("Env denylist", &s.cfg.Tools.Shell.Sandbox.EnvDenylist),
	)
}
