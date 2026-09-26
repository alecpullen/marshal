---
name: postmortem
description: Append semantic observations to a Marshal session postmortem report. Use only when the prompt gives you the path to a report JSON file and asks for the agent pass — review the session transcript and record friction you can see (wrong-tool choices, repeated failed strategies, prompt-confusion patterns) into the report's agent_observations field. Extraction only; never synthesize.
risk: workspace_write
---

# Postmortem Agent Pass

You are appending semantic observations to an existing postmortem report. The
deterministic extraction already ran; your job is to add what only a reader of
the transcript can see. You are recording, not judging.

## Procedure

1. **Read the report.** The prompt gives you the path to the report JSON file.
   Read it with `file.read` and find the `agent_observations` field. It is
   `null` until you fill it.
2. **Review the session transcript.** You already have the transcript in
   context. Read it for friction that the mechanical extraction could not
   capture: which tools the agent chose and whether a different tool was
   clearly the right one; strategies it repeated after they failed; places
   where it misread the prompt or the task goal. You may also consult
   `recall_history` if earlier turns are not visible.
3. **Write the observations.** Set `agent_observations` to a JSON array of
   **short strings** — one observation per string, each a concrete, factual
   statement of what happened. Keep each entry to a single sentence. Use
   `file.write_patch` to edit the report file in place, replacing the `null`
   value of `agent_observations` with your array. In the same patch, set
   `coverage.agent_pass` to `true` and `coverage.agent_pass_reason` to
   `"completed"`. Do not reformat or rewrite the rest of the file.

Example patch of the field:

```json
"agent_observations": [
  "used shell.run grep to find a symbol when symbols.find was available",
  "retried the same failing patch three times without reading the file first"
]
```

## What to observe

- **Wrong-tool choices** — the agent reached for a tool when a purpose-built
  one was available and would have been correct.
- **Repeated failed strategies** — the same action attempted again after it
  failed, unchanged.
- **Prompt-confusion patterns** — evidence the agent misread the request or
  lost the goal partway through.

## Extraction only

This pass is extraction only: do not summarize the session, do not rank
observations by importance, and do not recommend fixes, next steps, or
improvements. If you catch yourself writing "should have" or "better to", stop —
that is synthesis, and synthesis is explicitly out of scope.

## Field discipline

- Modify **only** the `agent_observations` field and the two coverage fields
  named above.
- Set `coverage.agent_pass` to `true` and `coverage.agent_pass_reason` to
  `"completed"`; leave every other `coverage` field as the extraction wrote it.
- Never change any other field of the report — not `session`, not
  `tool_failures`, not `token_waste`, not `schema_version`.
- If there is nothing observable, set `agent_observations` to an empty array
  `[]`; do not invent observations.
