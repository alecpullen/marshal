package settings

import "time"

func shellFrame(s *state) *frame {
	return newFrame("Shell", func() []*field {
		return []*field{
			func() *field {
				f := intField("shell.timeout", "Default timeout (s)",
					func() int { return s.cfg.Tools.Shell.DefaultTimeoutSeconds }, 0,
					func(v int) { s.cfg.Tools.Shell.DefaultTimeoutSeconds = v })
				f.desc = "default max seconds for shell command execution"
				return f
			}(),
			func() *field {
				f := bytesField("shell.max_output", "Max output size",
					func() int64 { return int64(s.cfg.Tools.Shell.MaxOutputBytes) },
					func(v int64) { s.cfg.Tools.Shell.MaxOutputBytes = int(v) })
				f.desc = "truncate shell output beyond this size"
				return f
			}(),
			func() *field {
				f := intField("shell.max_jobs", "Max background jobs",
					func() int { return s.cfg.Tools.Shell.MaxBackgroundJobs }, 0,
					func(v int) { s.cfg.Tools.Shell.MaxBackgroundJobs = v })
				f.desc = "max concurrent background shell processes"
				return f
			}(),
			func() *field {
				f := scalarField("shell.retention", "Background retention",
					func() string { return s.cfg.Tools.Shell.BackgroundRetention.String() },
					durationSetter(func(d time.Duration) { s.cfg.Tools.Shell.BackgroundRetention = d }))
				f.desc = "how long to keep completed background job output"
				return f
			}(),
			{id: "shell.allow_network", title: "Allow network", kind: kindToggle,
				desc:     "permit shell commands to access the network",
				keywords: []string{"internet"},
				getBool:  func() bool { return s.cfg.Tools.Shell.AllowNetwork },
				setBool:  func(v bool) { s.cfg.Tools.Shell.AllowNetwork = v }},
			{id: "shell.auto_approve", title: "Auto-approve shell", kind: kindToggle,
				desc:    "run classified-safe commands without confirmation",
				getBool: func() bool { return s.cfg.Tools.Shell.AutoApprove },
				setBool: func(v bool) { s.cfg.Tools.Shell.AutoApprove = v }},
			func() *field {
				f := enumField("shell.guardrail_argv0", "Dynamic argv0 guardrail",
					[]string{"deny", "confirm", "allow"},
					func() string { return s.cfg.Tools.Shell.GuardrailDynamicArgv0 },
					func(v string) { s.cfg.Tools.Shell.GuardrailDynamicArgv0 = v })
				f.desc = "policy for dynamically-resolved argv[0] commands"
				return f
			}(),
			func() *field {
				f := listDrill("shell.allow_commands", "Allow commands", &s.cfg.Tools.Shell.Allow.Commands)
				f.desc = "commands that run without confirmation"
				return f
			}(),
			func() *field {
				f := listDrill("shell.confirm_commands", "Confirm commands", &s.cfg.Tools.Shell.Confirm.Commands)
				f.desc = "commands that require user confirmation"
				return f
			}(),
			func() *field {
				f := listDrill("shell.deny_patterns", "Deny patterns", &s.cfg.Tools.Shell.Deny.Patterns)
				f.desc = "glob patterns that block command execution"
				return f
			}(),
		}
	})
}

func sandboxFrame(s *state) *frame {
	sb := &s.cfg.Tools.Shell.Sandbox
	return newFrame("Sandbox", func() []*field {
		return []*field{
			func() *field {
				f := enumField("sandbox.backend", "Backend",
					[]string{"restricted", "container", "passthrough"},
					func() string { return sb.Backend },
					func(v string) { sb.Backend = v })
				f.desc = "sandbox isolation backend"
				return f
			}(),
			func() *field {
				f := intField("sandbox.memory_mb", "Memory limit (MB)",
					func() int { return sb.MemoryLimitMB }, 0, func(v int) { sb.MemoryLimitMB = v })
				f.desc = "max memory for sandboxed processes"
				return f
			}(),
			func() *field {
				f := intField("sandbox.cpu_seconds", "CPU seconds",
					func() int { return sb.CPUSeconds }, 0, func(v int) { sb.CPUSeconds = v })
				f.desc = "max CPU time for sandboxed processes"
				return f
			}(),
			func() *field {
				f := intField("sandbox.max_processes", "Max processes",
					func() int { return sb.MaxProcesses }, 0, func(v int) { sb.MaxProcesses = v })
				f.desc = "max concurrent processes in the sandbox"
				return f
			}(),
			func() *field {
				f := intField("sandbox.file_size_mb", "File size limit (MB)",
					func() int { return sb.FileSizeLimitMB }, 0, func(v int) { sb.FileSizeLimitMB = v })
				f.desc = "max file size the sandbox may read or write"
				return f
			}(),
			func() *field {
				f := scalarField("sandbox.container_runtime", "Container runtime",
					func() string { return sb.ContainerRuntime },
					func(v string) error { sb.ContainerRuntime = v; return nil })
				f.desc = "container runtime binary (e.g. docker, podman)"
				return f
			}(),
			func() *field {
				f := scalarField("sandbox.container_image", "Container image",
					func() string { return sb.ContainerImage },
					func(v string) error { sb.ContainerImage = v; return nil })
				f.desc = "OCI image ref for sandboxed commands"
				return f
			}(),
			{id: "sandbox.allow_fallback", title: "Allow fallback", kind: kindToggle,
				desc:    "fall back to restricted backend when container is unavailable",
				getBool: func() bool { return sb.AllowFallback },
				setBool: func(v bool) { sb.AllowFallback = v }},
			{id: "sandbox.unsafe_passthrough", title: "Allow passthrough backend", kind: kindToggle, warn: true,
				desc:     "required opt-in for backend=passthrough — disables ALL process isolation",
				keywords: []string{"dangerous", "no sandbox"},
				getBool:  func() bool { return sb.UnsafePassthrough },
				setBool:  func(v bool) { sb.UnsafePassthrough = v }},
			func() *field {
				f := listDrill("sandbox.env_allowlist", "Env allowlist", &sb.EnvAllowlist)
				f.desc = "environment variables passed into the sandbox"
				return f
			}(),
			func() *field {
				f := listDrill("sandbox.env_denylist", "Env denylist", &sb.EnvDenylist)
				f.desc = "environment variables stripped from the sandbox"
				return f
			}(),
		}
	})
}
