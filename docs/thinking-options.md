# Model thinking controls

`/options` uses the selected model's discovered thinking controls. Opening the
panel refreshes its provider catalog in the background, subject to the remote
provider privacy setting. Cached controls are available immediately. A failed
refresh keeps cached controls; an edit in progress is not interrupted.

## Discovery

- Codex retains `supported_reasoning_levels` and `default_reasoning_level` from
  its account-specific model catalog, including new named levels.
- Native Ollama queries `/api/show` for each model's `thinking.values` and
  `thinking.default`. Boolean values appear as `on` and `off`; named levels
  appear exactly as reported. Compatible endpoints at ollama.com or port 11434
  use the same metadata. Up to four metadata requests run concurrently, each
  bounded to two seconds and the overall catalog request deadline.
- Anthropic reads thinking types and supported effort levels from `/v1/models`,
  following pagination. Adaptive models send `thinking.type = adaptive` and
  `output_config.effort`; legacy models use token budgets.
- Other compatible endpoints can supply a `thinking_options` object per model
  with `levels`, `default`, and `mode`. This is an optional extension, not part
  of the standard OpenAI model-list contract.

Model metadata is stored in the existing provider model cache. Configured
controls take precedence over discovery. Without discovered metadata Codex
falls back to low/medium/high. Other unknown models offer provider default,
plus any previously saved value marked unverified. A discovered empty list
means there is no selectable control.

## Configuration overrides

For a model whose endpoint does not advertise controls, add an override to its
user-global preset in `~/.config/marshal/config.toml`:

```toml
[models.presets."my-provider/my-model".thinking_options]
levels = ["low", "high", "max"]
default = "high"
mode = "effort"
```

Use only controls supported by that endpoint and model. Modes are `effort`
(named wire values), `toggle` (on/off), `budget` (legacy Anthropic budgets), and
`adaptive` (Anthropic effort). The default is informational; choosing
`default` clears the preset's effort override and sends no explicit control.

`off` sends false on native Ollama, none on compatible OpenAI endpoints, or
disables Anthropic thinking. It is offered only when reported or configured.
Named values discovered with mode `effort` are sent exactly as reported.
The Anthropic catalog does not report whether disabling adaptive thinking is
allowed, so Marshal does not infer an off option for those models.

Sources: [Ollama thinking](https://docs.ollama.com/capabilities/thinking),
[Ollama compatibility](https://docs.ollama.com/api/openai-compatibility),
[Anthropic model capabilities](https://platform.claude.com/docs/en/api/models),
[Anthropic effort](https://platform.claude.com/docs/en/build-with-claude/effort).
