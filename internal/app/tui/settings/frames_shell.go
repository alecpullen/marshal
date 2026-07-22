package settings

import "time"

func shellFrame(s *state) *frame {
	return newFrame("Shell", func() []*field {
		return []*field{
			intField("shell.timeout", "Default timeout (s)",
				func() int { return s.cfg.Tools.Shell.DefaultTimeoutSeconds }, 0,
				func(v int) { s.cfg.Tools.Shell.DefaultTimeoutSeconds = v }),
			intField("shell.max_output", "Max output bytes",
				func() int { return s.cfg.Tools.Shell.MaxOutputBytes }, 0,
				func(v int) { s.cfg.Tools.Shell.MaxOutputBytes = v }),
			intField("shell.max_jobs", "Max background jobs",
				func() int { return s.cfg.Tools.Shell.MaxBackgroundJobs }, 0,
				func(v int) { s.cfg.Tools.Shell.MaxBackgroundJobs = v }),
			scalarField("shell.retention", "Background retention",
				func() string { return s.cfg.Tools.Shell.BackgroundRetention.String() },
				durationSetter(func(d time.Duration) { s.cfg.Tools.Shell.BackgroundRetention = d })),
			{id: "shell.allow_network", title: "Allow network", kind: kindToggle,
				keywords: []string{"internet"},
				getBool:  func() bool { return s.cfg.Tools.Shell.AllowNetwork },
				setBool:  func(v bool) { s.cfg.Tools.Shell.AllowNetwork = v }},
			{id: "shell.auto_approve", title: "Auto-approve shell", kind: kindToggle,
				desc:    "run classified-safe commands without confirmation",
				getBool: func() bool { return s.cfg.Tools.Shell.AutoApprove },
				setBool: func(v bool) { s.cfg.Tools.Shell.AutoApprove = v }},
			enumField("shell.guardrail_argv0", "Dynamic argv0 guardrail",
				[]string{"deny", "confirm", "allow"},
				func() string { return s.cfg.Tools.Shell.GuardrailDynamicArgv0 },
				func(v string) { s.cfg.Tools.Shell.GuardrailDynamicArgv0 = v }),
			listDrill("shell.allow_commands", "Allow commands", &s.cfg.Tools.Shell.Allow.Commands),
			listDrill("shell.confirm_commands", "Confirm commands", &s.cfg.Tools.Shell.Confirm.Commands),
			listDrill("shell.deny_patterns", "Deny patterns", &s.cfg.Tools.Shell.Deny.Patterns),
		}
	})
}

func sandboxFrame(s *state) *frame {
	sb := &s.cfg.Tools.Shell.Sandbox
	return newFrame("Sandbox", func() []*field {
		return []*field{
			enumField("sandbox.backend", "Backend",
				[]string{"restricted", "container", "passthrough"},
				func() string { return sb.Backend },
				func(v string) { sb.Backend = v }),
			intField("sandbox.memory_mb", "Memory limit (MB)",
				func() int { return sb.MemoryLimitMB }, 0, func(v int) { sb.MemoryLimitMB = v }),
			intField("sandbox.cpu_seconds", "CPU seconds",
				func() int { return sb.CPUSeconds }, 0, func(v int) { sb.CPUSeconds = v }),
			intField("sandbox.max_processes", "Max processes",
				func() int { return sb.MaxProcesses }, 0, func(v int) { sb.MaxProcesses = v }),
			intField("sandbox.file_size_mb", "File size limit (MB)",
				func() int { return sb.FileSizeLimitMB }, 0, func(v int) { sb.FileSizeLimitMB = v }),
			scalarField("sandbox.container_runtime", "Container runtime",
				func() string { return sb.ContainerRuntime },
				func(v string) error { sb.ContainerRuntime = v; return nil }),
			scalarField("sandbox.container_image", "Container image",
				func() string { return sb.ContainerImage },
				func(v string) error { sb.ContainerImage = v; return nil }),
			{id: "sandbox.allow_fallback", title: "Allow fallback", kind: kindToggle,
				getBool: func() bool { return sb.AllowFallback },
				setBool: func(v bool) { sb.AllowFallback = v }},
			{id: "sandbox.unsafe_passthrough", title: "Allow passthrough backend", kind: kindToggle, warn: true,
				desc:     "required opt-in for backend=passthrough — disables ALL process isolation",
				keywords: []string{"dangerous", "no sandbox"},
				getBool:  func() bool { return sb.UnsafePassthrough },
				setBool:  func(v bool) { sb.UnsafePassthrough = v }},
			listDrill("sandbox.env_allowlist", "Env allowlist", &sb.EnvAllowlist),
			listDrill("sandbox.env_denylist", "Env denylist", &sb.EnvDenylist),
		}
	})
}
