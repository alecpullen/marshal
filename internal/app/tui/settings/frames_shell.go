package settings

import "time"

func shellFrame(s *state) *frame {
	return newFrame("Shell", func() []*field {
		return []*field{
			func() *field {
				f := intField("shell.timeout", "Default timeout (s)",
					func() int { return s.cfg.Tools.Shell.DefaultTimeoutSeconds }, 0,
					func(v int) { s.cfg.Tools.Shell.DefaultTimeoutSeconds = v })
				f.TomlPath = "tools.shell.default_timeout_seconds"
				f.Desc = "default max seconds for shell command execution"
				return f
			}(),
			func() *field {
				f := bytesField("shell.max_output", "Max output size",
					func() int64 { return int64(s.cfg.Tools.Shell.MaxOutputBytes) },
					func(v int64) { s.cfg.Tools.Shell.MaxOutputBytes = int(v) })
				f.TomlPath = "tools.shell.max_output_bytes"
				f.Desc = "truncate shell output beyond this size"
				return f
			}(),
			func() *field {
				f := intField("shell.max_jobs", "Max background jobs",
					func() int { return s.cfg.Tools.Shell.MaxBackgroundJobs }, 0,
					func(v int) { s.cfg.Tools.Shell.MaxBackgroundJobs = v })
				f.TomlPath = "tools.shell.max_background_jobs"
				f.Desc = "max concurrent background shell processes"
				return f
			}(),
			func() *field {
				f := scalarField("shell.retention", "Background retention",
					func() string { return s.cfg.Tools.Shell.BackgroundRetention.String() },
					durationSetter(func(d time.Duration) { s.cfg.Tools.Shell.BackgroundRetention = d }))
				f.TomlPath = "tools.shell.background_retention"
				f.Desc = "how long to keep completed background job output"
				return f
			}(),
			{ID: "shell.allow_network", Title: "Allow network", Kind: kindToggle,
				TomlPath: "tools.shell.allow_network",
				Desc:     "permit shell commands to access the network",
				Keywords: []string{"internet"},
				GetBool:  func() bool { return s.cfg.Tools.Shell.AllowNetwork },
				SetBool:  func(v bool) { s.cfg.Tools.Shell.AllowNetwork = v }},
			{ID: "shell.auto_approve", Title: "Auto-approve shell", Kind: kindToggle,
				TomlPath: "tools.shell.auto_approve",
				Desc:     "run classified-safe commands without confirmation",
				GetBool:  func() bool { return s.cfg.Tools.Shell.AutoApprove },
				SetBool:  func(v bool) { s.cfg.Tools.Shell.AutoApprove = v }},
			func() *field {
				f := enumField("shell.guardrail_argv0", "Dynamic argv0 guardrail",
					[]string{"deny", "confirm", "allow"},
					func() string { return s.cfg.Tools.Shell.GuardrailDynamicArgv0 },
					func(v string) { s.cfg.Tools.Shell.GuardrailDynamicArgv0 = v })
				f.TomlPath = "tools.shell.guardrail_dynamic_argv0"
				f.Desc = "policy for dynamically-resolved argv[0] commands"
				return f
			}(),
			func() *field {
				f := listDrill("shell.allow_commands", "Allow commands", &s.cfg.Tools.Shell.Allow.Commands)
				f.TomlPath = "tools.shell.allow.commands"
				f.Desc = "commands that run without confirmation"
				return f
			}(),
			func() *field {
				f := listDrill("shell.confirm_commands", "Confirm commands", &s.cfg.Tools.Shell.Confirm.Commands)
				f.TomlPath = "tools.shell.confirm.commands"
				f.Desc = "commands that require user confirmation"
				return f
			}(),
			func() *field {
				f := listDrill("shell.deny_patterns", "Deny patterns", &s.cfg.Tools.Shell.Deny.Patterns)
				f.TomlPath = "tools.shell.deny.patterns"
				f.Desc = "glob patterns that block command execution"
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
				f.TomlPath = "tools.shell.sandbox.backend"
				f.Desc = "sandbox isolation backend"
				return f
			}(),
			func() *field {
				f := intField("sandbox.memory_mb", "Memory limit (MB)",
					func() int { return sb.MemoryLimitMB }, 0, func(v int) { sb.MemoryLimitMB = v })
				f.TomlPath = "tools.shell.sandbox.memory_limit_mb"
				f.Desc = "max memory for sandboxed processes"
				return f
			}(),
			func() *field {
				f := intField("sandbox.cpu_seconds", "CPU seconds",
					func() int { return sb.CPUSeconds }, 0, func(v int) { sb.CPUSeconds = v })
				f.TomlPath = "tools.shell.sandbox.cpu_seconds"
				f.Desc = "max CPU time for sandboxed processes"
				return f
			}(),
			func() *field {
				f := intField("sandbox.max_processes", "Max processes",
					func() int { return sb.MaxProcesses }, 0, func(v int) { sb.MaxProcesses = v })
				f.TomlPath = "tools.shell.sandbox.max_processes"
				f.Desc = "max concurrent processes in the sandbox"
				return f
			}(),
			func() *field {
				f := intField("sandbox.file_size_mb", "File size limit (MB)",
					func() int { return sb.FileSizeLimitMB }, 0, func(v int) { sb.FileSizeLimitMB = v })
				f.TomlPath = "tools.shell.sandbox.file_size_limit_mb"
				f.Desc = "max file size the sandbox may read or write"
				return f
			}(),
			func() *field {
				f := scalarField("sandbox.container_runtime", "Container runtime",
					func() string { return sb.ContainerRuntime },
					func(v string) error { sb.ContainerRuntime = v; return nil })
				f.TomlPath = "tools.shell.sandbox.container_runtime"
				f.Desc = "container runtime binary (e.g. docker, podman)"
				return f
			}(),
			func() *field {
				f := scalarField("sandbox.container_image", "Container image",
					func() string { return sb.ContainerImage },
					func(v string) error { sb.ContainerImage = v; return nil })
				f.TomlPath = "tools.shell.sandbox.container_image"
				f.Desc = "OCI image ref for sandboxed commands"
				return f
			}(),
			{ID: "sandbox.allow_fallback", Title: "Allow fallback", Kind: kindToggle,
				TomlPath: "tools.shell.sandbox.allow_fallback",
				Desc:     "fall back to restricted backend when container is unavailable",
				GetBool:  func() bool { return sb.AllowFallback },
				SetBool:  func(v bool) { sb.AllowFallback = v }},
			{ID: "sandbox.unsafe_passthrough", Title: "Allow passthrough backend", Kind: kindToggle, Warn: true,
				TomlPath: "tools.shell.sandbox.unsafe_passthrough",
				Desc:     "required opt-in for backend=passthrough — disables ALL process isolation",
				Keywords: []string{"dangerous", "no sandbox"},
				GetBool:  func() bool { return sb.UnsafePassthrough },
				SetBool:  func(v bool) { sb.UnsafePassthrough = v }},
			func() *field {
				f := listDrill("sandbox.env_allowlist", "Env allowlist", &sb.EnvAllowlist)
				f.TomlPath = "tools.shell.sandbox.env_allowlist"
				f.Desc = "environment variables passed into the sandbox"
				return f
			}(),
			func() *field {
				f := listDrill("sandbox.env_denylist", "Env denylist", &sb.EnvDenylist)
				f.TomlPath = "tools.shell.sandbox.env_denylist"
				f.Desc = "environment variables stripped from the sandbox"
				return f
			}(),
		}
	})
}
