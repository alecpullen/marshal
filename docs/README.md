# Marshal Documentation Pack

Marshal is a local-first TUI coding agent written in Go. It is designed for developers who want to supply their own inference backend, especially local providers such as Ollama, llama.cpp, vLLM, and LM Studio, while still allowing optional hosted API providers such as OpenRouter or other OpenAI-compatible APIs.

Marshal combines:

- terminal-native coding workflows
- local-first model usage
- smart repository context management
- controlled shell and tool execution
- persistent project knowledge
- role-based model presets
- swarm/multi-agent capabilities with specialist roles
- MCP/plugin ecosystem for extensibility
- loadable skill-based instruction sets

## Document index

1. [Product Vision](./01-product-vision.md)
2. [System Architecture](./02-system-architecture.md)
3. [Provider and Model Routing](./03-provider-and-model-routing.md)
4. [Tooling and Shell Safety](./04-tooling-and-shell-safety.md)
5. [Context and Project Knowledge](./05-context-and-project-knowledge.md)
6. [TUI Design](./06-tui-design.md)
7. [Agent Runtime and Swarm Planning](./07-agent-runtime-and-swarm.md)
8. [Roadmap and Milestones](./08-roadmap-and-milestones.md)
9. [Configuration Examples](./09-configuration-examples.md)
10. [MVP Implementation Checklist](./10-mvp-implementation-checklist.md)
11. [Roadmap and Future Enhancements](./11-roadmap-and-future-enhancements.md)

## Working assumptions

- The app is written in Go.
- The TUI uses Bubble Tea.
- The model transport is OpenAI-compatible chat completions.
- SQLite is used for local project/session state.
- Tree-sitter is used for structural repository intelligence.
- The MVP is a functional single-agent coding assistant; swarm features are built and stable.

## One-sentence vision

Marshal is a local-first terminal coding agent that combines user-owned inference, safe tool execution, structural repository intelligence, specialist agent swarms, and an extensible plugin system for serious software work.
