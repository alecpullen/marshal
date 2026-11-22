# Conversations in Marshal Studio

Design approved · 21 November 2026

[Implementation plan](plans/2026-11-21-agent-first-studio-plan.md)

## Starting work

Opening a project takes the user to its project agent. Restore their last active thread when possible. On a first visit, show a focused composer with the project name above it. **New** opens an empty conversation in that project. Create the session on first send and keep the draft if startup fails. If Studio has no project context, offer the last selected project visibly, ask the user to choose one if necessary, or offer to connect their first repository.

“Find out why CI is failing” should start work with the project's configured defaults. Place overrides under **Options**. Keep issue, recipe and draft selection available there. Bring up planning or a work item when the request calls for it. An explicit request to plan produces a plan for discussion before implementation.

Keep **Marshal**, **Overview**, **Needs you**, and **Pull requests** in primary navigation alongside projects and recent threads. Overview retains the fleet dashboard. Rename the existing Inbox to Needs you throughout Studio, preserving its links, filters, inline answers, and ready-for-review pull requests. Marshal can summarize the same queue in conversation. Pull requests retains its dedicated review screen and check groups. Keep boards, settings, and other management views available through secondary navigation and their existing links. In a thread, show **Stop** while work runs. Put permission choices beside the relevant ask and **View diff** beside the change. Use the existing composer, ask card, thread rows and inspector.

## Asking Marshal to coordinate

Marshal has its own conversation. Name the recipient above the composer on both surfaces. A project agent handles work within its project. An explicit “Have Marshal coordinate this with the CLI project” hands off the task. Identify the source thread, the requested outcome, relevant findings, and the target projects. The user can open the coordinator conversation from the source thread. Switching views preserves the draft in each.

For example, a user working in the API thread discovers a client compatibility issue. Marshal can link the existing API thread, start a CLI agent with the compatibility requirement, and show each assignment with its project, status and thread link. The user can correct the CLI agent directly. Marshal follows its actual result and reports any remaining incompatibility back in the source thread. Share the task facts each recipient needs and preserve the visibility rules for linked material.

When the request and targets are clear, begin the authorized assignments immediately. When a target is ambiguous, ask a focused question before dispatching it. Each project retains its own permissions, trust and approvals. Surface waiting decisions as links to the original ask. “What needs my attention?” reads current asks across accessible projects and opens the exact thread or review that needs an answer. It starts no work.

## Runtime and recovery

Each coordinated task has its own Marshal thread. The sidebar entry resumes the last one on the selected host, with recent tasks and **New** available as in a project. A handoff creates a task linked to its source. Replies in its coordinator thread address that task.

Marshal may follow a linked thread's progress. It starts a dedicated worker for a new assignment unless the user explicitly directs an existing thread to take it on. **Stop coordinated task** stops further dispatch and requests cancellation of workers that task created. Independently started threads keep their own stop control.

The bridge already exposes agent operations through MCP, including spawn, send, status, result and cancel. The existing thread composer supports steering and stopping. The proposed integration must bind coordinator actions to the user's authorized project scope and keep durable links among the coordinator task, source thread and assignments. Keep orchestration on the agent and daemon side of the client boundary. Use configured providers and routes.

Show an assignment as running only after acknowledgment. After reconnect or retry, reconcile the saved assignments before dispatch so a partially successful handoff cannot launch a duplicate worker. A failed or inaccessible project gets its own status and recovery action. Other authorized assignments may continue. Distinguish stale state from completion and link results to the originating thread. Label cancellation by its scope so the user can stop one worker or the coordinated task.

## Approaches considered

A coordinator-only home would give users one address for all work, but add a handoff to ordinary single-project requests. A project-only home would keep that path short and leave cross-project coordination to the user. The selected design gives each project its own conversation and makes Marshal an explicit destination when the task spans projects.

## Delivery and acceptance

1. Start with the project entry and composer. A user can open a project, request a CI investigation, steer or stop the agent, and inspect its diff without opening a setup dialog. Existing project and thread links still resolve.
2. Add Marshal's read-only attention summary. With two waiting projects, the user can find both decisions and answer each at its source. With an unavailable host, show the limits of the summary.
3. Add delegation and handoff. A request from the API thread starts one assignment per intended project, reuses relevant ongoing work, and survives a reload or partial dispatch. A project approval pauses the affected action; a direct correction in a worker thread reaches Marshal's eventual summary.

The proposed first release scopes coordination to projects on the selected host. The storage schema, event delivery and exact placement of secondary navigation are implementation decisions. Update the Studio design system alongside changes to its navigation and thread layouts.
