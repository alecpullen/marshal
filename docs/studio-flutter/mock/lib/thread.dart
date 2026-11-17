import 'package:cupertino_ui/cupertino_ui.dart' show CupertinoIcons;
import 'package:material_ui/material_ui.dart';

import 'data.dart';
import 'inbox.dart';
import 'inspector.dart';
import 'pulls.dart';
import 'state.dart';
import 'widgets.dart';

/// One screen for every kind of thread. An agent, a factory work item and a
/// plan run differ only in the strip under the header and the inspector tabs.
class ThreadPage extends StatelessWidget {
  const ThreadPage({super.key, required this.thread, required this.narrow});
  final Thread thread;
  final bool narrow;

  @override
  Widget build(BuildContext context) {
    final studio = context.studio;
    final showInspector = studio.inspectorOpen && !narrow;
    final centre = Column(children: [
      _Header(thread: thread, narrow: narrow),
      const Hairline(),
      if (thread.kind == Kind.work) ...[_StageStrip(thread: thread), const Hairline()],
      Expanded(
        child: SelectionArea(
          child: ListView(
            padding: const EdgeInsets.symmetric(vertical: 20),
            children: [
              Center(
                child: ConstrainedBox(
                  constraints: const BoxConstraints(maxWidth: 760),
                  child: Padding(
                    padding: const EdgeInsets.symmetric(horizontal: 24),
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        if (thread.status == Status.ready) ReadyReview(thread: thread),
                        for (final pull in pulls.where((p) => p.thread == thread.id && p.failed))
                          Padding(padding: const EdgeInsets.only(bottom: 20), child: PrCard(pull: pull)),
                        ...switch (thread.kind) {
                        Kind.agent => _agentTranscript(context, thread),
                        Kind.work => _workActivity(context, thread),
                        Kind.run => _runLanes(context, thread),
                        },
                      ],
                    ),
                  ),
                ),
              ),
            ],
          ),
        ),
      ),
      _Composer(thread: thread),
    ]);

    if (!showInspector) return centre;
    if (studio.inspectorWide) {
      return Row(children: [
        SizedBox(width: 300, child: centre),
        const Hairline(vertical: true),
        Expanded(child: Inspector(thread: thread)),
      ]);
    }
    return Row(children: [
      Expanded(child: centre),
      const Hairline(vertical: true),
      SizedBox(width: 400, child: Inspector(thread: thread)),
    ]);
  }
}

class _Header extends StatelessWidget {
  const _Header({required this.thread, required this.narrow});
  final Thread thread;
  final bool narrow;

  @override
  Widget build(BuildContext context) {
    final studio = context.studio;
    final s = context.s;
    final project = projectById(thread.project);
    return Padding(
      padding: const EdgeInsets.fromLTRB(20, 10, 12, 10),
      child: Row(children: [
        Expanded(
          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            Row(children: [
              Tap(
                onTap: () => studio.go(Place.project, project.id),
                builder: (context, hovered) =>
                    Text(project.name, style: s.text(12, color: hovered ? s.fg : s.muted)),
              ),
              Text('  ›  ', style: s.text(12, color: s.faint)),
              Text(kindLabel(thread.kind), style: s.text(12, color: s.muted)),
              if (!narrow) ...[
                Text('  ·  ', style: s.text(12, color: s.faint)),
                Flexible(
                  child: Text('${thread.branch}  ·  ${thread.env}  ·  ${thread.host}',
                      overflow: TextOverflow.ellipsis, style: s.mono(11.5, color: s.muted)),
                ),
              ],
            ]),
            const SizedBox(height: 3),
            Row(children: [
              Dot(thread.status),
              const SizedBox(width: 8),
              Flexible(
                flex: 0,
                child: Text(thread.title,
                    overflow: TextOverflow.ellipsis, style: s.text(15, weight: FontWeight.w600)),
              ),
              const SizedBox(width: 10),
              Expanded(
                child: Text(thread.headline, overflow: TextOverflow.ellipsis, style: s.text(12.5, color: s.muted)),
              ),
            ]),
          ]),
        ),
        const IconBtn(CupertinoIcons.share, tooltip: 'Share a read-only status link'),
        const IconBtn(CupertinoIcons.ellipsis, tooltip: 'Stop, hold, hand to terminal, archive'),
        if (!narrow)
          IconBtn(
            CupertinoIcons.sidebar_right,
            tooltip: 'Inspector  ⌘\\',
            active: studio.inspectorOpen,
            onTap: () => studio.set(() => studio.inspectorOpen = !studio.inspectorOpen),
          ),
      ]),
    );
  }
}

/// Work items only: where the item is in its factory, with no decoration.
class _StageStrip extends StatelessWidget {
  const _StageStrip({required this.thread});
  final Thread thread;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: 20, vertical: 9),
      child: Row(children: [
        for (var i = 0; i < stages.length; i++) ...[
          if (i > 0)
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 10),
              child: Icon(CupertinoIcons.chevron_right, size: 9, color: s.faint),
            ),
          Icon(
            i < thread.stage
                ? CupertinoIcons.checkmark_circle_fill
                : i == thread.stage
                    ? CupertinoIcons.circle_fill
                    : CupertinoIcons.circle,
            size: 11,
            color: i < thread.stage
                ? s.ok
                : i == thread.stage
                    ? statusColor(s, thread.status)
                    : s.faint,
          ),
          const SizedBox(width: 6),
          Text(stages[i],
              style: s.text(12.5,
                  color: i <= thread.stage ? s.fg : s.muted,
                  weight: i == thread.stage ? FontWeight.w600 : FontWeight.w400)),
        ],
        const Spacer(),
        Text('${thread.done} of ${thread.total} PRs · repair 1 of 5 · ${thread.elapsed}',
            style: s.mono(11.5, color: s.muted)),
      ]),
    );
  }
}

// ---------------------------------------------------------------- transcript

Widget _user(BuildContext context, String text) {
  final s = context.s;
  return Align(
    alignment: Alignment.centerRight,
    child: Container(
      margin: const EdgeInsets.only(left: 80, bottom: 18),
      padding: const EdgeInsets.symmetric(horizontal: 13, vertical: 9),
      decoration: BoxDecoration(
        border: Border.all(color: s.border),
        borderRadius: BorderRadius.circular(s.radius),
      ),
      child: Text(text, style: s.text(13.5, height: 1.5)),
    ),
  );
}

Widget _say(BuildContext context, String text) => Padding(
      padding: const EdgeInsets.only(bottom: 12),
      child: Text(text, style: context.s.text(13.5, height: 1.55)),
    );

Widget _step(BuildContext context, String number, String headline, {String? took, bool live = false}) {
  final s = context.s;
  return Padding(
    padding: const EdgeInsets.only(top: 6, bottom: 6),
    child: Row(children: [
      SizedBox(width: 30, child: Text(number, style: s.mono(11.5, color: s.faint))),
      if (live) ...[const Dot(Status.running, size: 7), const SizedBox(width: 8)],
      Expanded(child: Text(headline, style: s.text(13.5, weight: FontWeight.w600))),
      if (took != null) Text(took, style: s.mono(11, color: s.faint)),
    ]),
  );
}

Widget _tool(BuildContext context, IconData icon, String call, String result, {Color? color, bool selected = false}) {
  final s = context.s;
  return Tap(
    onTap: () => context.studio.set(() {
      context.studio.inspectorOpen = true;
      context.studio.inspectorTab = 4;
    }),
    builder: (context, hovered) => Container(
      margin: const EdgeInsets.only(left: 30, bottom: 2),
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 4),
      decoration: BoxDecoration(
        color: selected ? s.selected : (hovered ? s.hover : null),
        borderRadius: BorderRadius.circular(s.control),
      ),
      child: Row(children: [
        Icon(icon, size: 12, color: s.muted),
        const SizedBox(width: 8),
        Expanded(child: Text(call, overflow: TextOverflow.ellipsis, style: s.mono(12))),
        const SizedBox(width: 10),
        Text(result, style: s.mono(11.5, color: color ?? s.muted)),
      ]),
    ),
  );
}

List<Widget> _agentTranscript(BuildContext context, Thread thread) {
  final s = context.s;
  final ask = inbox.where((i) => i.thread == thread.id).toList();
  return [
    _user(context, 'Clean up test fixtures nobody uses any more. Check with me before deleting anything that looks deliberate.'),
    _step(context, '1', 'Mapped the fixtures and who reads them', took: '41s'),
    _tool(context, CupertinoIcons.search, 'search  "testdata/" --type go', '212 matches'),
    _tool(context, CupertinoIcons.doc_text, 'read  internal/acp/acptest/fixtures.go', '148 lines'),
    _tool(context, CupertinoIcons.chevron_right, r'go list -test ./... | grep testdata', 'exit 0'),
    const SizedBox(height: 8),
    _say(context,
        'There are 96 fixture files. 82 are loaded by a test. The other 14 all sit under testdata/legacy and are referenced nowhere in Go code, scripts or docs.'),
    _step(context, '2', 'Removed the fixtures that are plainly dead', took: '1m 08s'),
    _tool(context, CupertinoIcons.pencil, 'edit  internal/viewmodel/testdata/index.json', '+0 −9', selected: true),
    _tool(context, CupertinoIcons.trash, 'delete  internal/repo/testdata/old_symbols/*.golden', '6 files'),
    _tool(context, CupertinoIcons.chevron_right, 'scripts/test.sh', '412 passed', color: s.ok),
    const SizedBox(height: 8),
    _say(context,
        'Tests pass with those gone. The legacy directory is different: its README says it is kept "for the migration check", but no migration check exists in the repository today.'),
    _step(context, '3', 'Waiting for your call on testdata/legacy', live: true),
    const SizedBox(height: 6),
    for (final item in ask)
      Padding(
        padding: const EdgeInsets.only(left: 30, bottom: 12),
        child: AskCard(item: item, showSource: false),
      ),
    if (ask.isEmpty)
      Padding(
        padding: const EdgeInsets.only(left: 30),
        child: Text(thread.headline, style: s.text(13, color: s.muted)),
      ),
  ];
}

List<Widget> _workActivity(BuildContext context, Thread thread) {
  final s = context.s;
  final ask = inbox.where((i) => i.thread == thread.id).toList();

  Widget event(String who, Color color, String text, String when, {String? detail}) => Padding(
        padding: const EdgeInsets.only(bottom: 14),
        child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
          SizedBox(
            width: 92,
            child: Text(who, style: s.text(12, color: color, weight: FontWeight.w600)),
          ),
          Expanded(
            child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Text(text, style: s.text(13.5, height: 1.45)),
              if (detail != null)
                Padding(
                  padding: const EdgeInsets.only(top: 3),
                  child: Text(detail, style: s.text(12.5, color: s.muted, height: 1.45)),
                ),
            ]),
          ),
          const SizedBox(width: 10),
          Text(when, style: s.mono(11, color: s.faint)),
        ]),
      );

  return [
    for (final item in ask)
      Padding(padding: const EdgeInsets.only(bottom: 20), child: AskCard(item: item, showSource: false)),
    Label('Activity', trailing: Text('Planner, 2 implementers, 1 reviewer', style: s.text(11.5, color: s.muted))),
    const SizedBox(height: 14),
    event('You', s.muted, 'Approved the handoff: 5 PRs, repair allowance 5, planner may answer agent questions.', '1h 12m'),
    event('Planner', s.fg, 'Split the work into 5 pull requests and started the first two.', '1h 11m',
        detail: 'Reconnect replay, then approval persistence. The last three wait on the replay contract.'),
    event('Implementer A', s.fg, 'Opened draft PR #121: reconnect replay.', '52m', detail: '+214 −38 in 9 files · CI running'),
    event('Reviewer', s.fg, 'One finding on PR #121: approvals answered during a reconnect are dropped.', '31m',
        detail: 'Should fix. Sent back to Implementer A as repair 1 of 5.'),
    event('Implementer A', s.fg, 'Repaired PR #121. CI passed on the new head.', '19m'),
    if (ask.isNotEmpty)
      event('Planner', s.fg, 'Raised a scope decision and paused the PRs that depend on it.', '12m',
          detail: 'Implementer B keeps working on the part that does not depend on the answer.')
    else
      event('Implementer B', s.fg, thread.headline, '2m'),
  ];
}

List<Widget> _runLanes(BuildContext context, Thread thread) {
  final s = context.s;
  const columns = ['Implement', 'Verify', 'Review', 'Commit'];
  const tasks = [
    ('1', 'Version handshake on attach', [2, 2, 2, 2], '6afd241f'),
    ('2', 'Refuse a mismatched child', [2, 2, 2, 2], '9206749b'),
    ('3', 'Drain sessions before upgrade', [2, 2, 1, 0], ''),
    ('4', 'Restart children on the new binary', [0, 0, 0, 0], ''),
    ('5', 'daemon upgrade command', [0, 0, 0, 0], ''),
    ('6', 'Tray menu entry', [0, 0, 0, 0], ''),
    ('7', 'Docs and status output', [0, 0, 0, 0], ''),
  ];

  Widget cell(int state) => Expanded(
        child: Row(children: [
          Icon(
            state == 2
                ? CupertinoIcons.checkmark_circle_fill
                : state == 1
                    ? CupertinoIcons.circle_fill
                    : CupertinoIcons.circle,
            size: 11,
            color: state == 2
                ? s.ok
                : state == 1
                    ? s.running
                    : s.border,
          ),
          if (state == 1) ...[
            const SizedBox(width: 6),
            Text('fix round 1', style: s.text(11.5, color: s.muted)),
          ],
        ]),
      );

  return [
    Label('Tasks', trailing: Text('Lanes · Graph · Timeline', style: s.text(11.5, color: s.muted))),
    const SizedBox(height: 10),
    Box(
      padding: EdgeInsets.zero,
      child: Column(children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(14, 9, 14, 7),
          child: Row(children: [
            Expanded(flex: 4, child: Text('Task', style: s.text(11.5, color: s.muted))),
            for (final c in columns) Expanded(child: Text(c, style: s.text(11.5, color: s.muted))),
            SizedBox(width: 70, child: Text('SHA', style: s.text(11.5, color: s.muted))),
          ]),
        ),
        for (final t in tasks) ...[
          const Hairline(),
          Padding(
            padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 9),
            child: Row(children: [
              Expanded(
                flex: 4,
                child: Row(children: [
                  SizedBox(width: 20, child: Text(t.$1, style: s.mono(11.5, color: s.faint))),
                  Expanded(
                    child: Text(t.$2,
                        overflow: TextOverflow.ellipsis,
                        style: s.text(13, color: t.$3.first == 0 ? s.muted : s.fg)),
                  ),
                ]),
              ),
              for (final state in t.$3) cell(state),
              SizedBox(width: 70, child: Text(t.$4.isEmpty ? '' : t.$4.substring(0, 7), style: s.mono(11.5, color: s.muted))),
            ]),
          ),
        ],
      ]),
    ),
    const SizedBox(height: 20),
    const Label('Now'),
    const SizedBox(height: 8),
    _step(context, '3', 'Reviewer asked for one fix on task 3', live: true),
    _say(context,
        'The drain waits on sessions but not on a pending permission request, so an upgrade could strand a question. The implementer is adding the wait and a test for it.'),
  ];
}

class _Composer extends StatelessWidget {
  const _Composer({required this.thread});
  final Thread thread;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    final hint = switch (thread.kind) {
      Kind.agent => thread.status == Status.running ? 'Steer the agent…' : 'Reply to the agent…',
      Kind.work => 'Steer the planner, or pick an agent…',
      Kind.run => 'Steer this run…',
    };
    return Center(
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 760),
        child: Padding(
          padding: const EdgeInsets.fromLTRB(24, 0, 24, 16),
          child: Container(
            padding: const EdgeInsets.fromLTRB(12, 4, 8, 8),
            decoration: BoxDecoration(
              border: Border.all(color: s.border),
              borderRadius: BorderRadius.circular(s.radius),
            ),
            child: Column(children: [
              TextField(
                minLines: 1,
                maxLines: 6,
                style: s.text(13.5),
                cursorColor: s.accent,
                decoration: InputDecoration(
                  hintText: hint,
                  hintStyle: s.text(13.5, color: s.faint),
                  border: InputBorder.none,
                  isDense: true,
                  contentPadding: const EdgeInsets.symmetric(vertical: 10),
                ),
              ),
              Row(children: [
                if (thread.kind == Kind.work) ...[
                  const Pill('To: Planner', icon: CupertinoIcons.chevron_down, tooltip: 'Who receives this message'),
                  const SizedBox(width: 6),
                ],
                Pill(thread.kind == Kind.agent ? 'Auto' : 'Factory authority', icon: CupertinoIcons.shield, tooltip: 'Permission mode'),
                const SizedBox(width: 6),
                Flexible(
                  child: Align(
                    alignment: Alignment.centerLeft,
                    child: Pill(thread.model, icon: CupertinoIcons.sparkles, tooltip: 'Model'),
                  ),
                ),
                const SizedBox(width: 8),
                const IconBtn(CupertinoIcons.paperclip, tooltip: 'Attach'),
                const IconBtn(CupertinoIcons.mic, tooltip: 'Dictate'),
                const SizedBox(width: 4),
                if (thread.status == Status.running) ...[
                  const Btn('Stop', icon: CupertinoIcons.stop),
                  const SizedBox(width: 6),
                ],
                Btn(thread.status == Status.running ? 'Steer' : 'Send', primary: true, icon: CupertinoIcons.arrow_up),
              ]),
            ]),
          ),
        ),
      ),
    );
  }
}
