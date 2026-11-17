import 'package:cupertino_ui/cupertino_ui.dart' show CupertinoIcons;
import 'package:material_ui/material_ui.dart';

import 'data.dart';
import 'state.dart';
import 'widgets.dart';

/// The right-hand panel. Its tabs depend on the kind of thread; everything a
/// separate page used to hold about one piece of work lives here instead.
class Inspector extends StatelessWidget {
  const Inspector({super.key, required this.thread});
  final Thread thread;

  @override
  Widget build(BuildContext context) {
    final studio = context.studio;
    final tabs = switch (thread.kind) {
      Kind.agent => const ['Changes', 'Files', 'Terminal', 'Preview', 'Inspect'],
      Kind.work => const ['Plan', 'Agents', 'Checks', 'Changes', 'Pull requests'],
      Kind.run => const ['Plan', 'Roles', 'Changes', 'Terminal', 'Inspect'],
    };
    final index = studio.inspectorTab.clamp(0, tabs.length - 1);
    final body = switch (tabs[index]) {
      'Changes' => _Changes(thread: thread),
      'Files' => const _Files(),
      'Terminal' => const _Terminal(),
      'Preview' => const _Preview(),
      'Inspect' => const _Inspect(),
      'Plan' => _Plan(thread: thread),
      'Agents' || 'Roles' => const _Agents(),
      'Checks' => const _Checks(),
      _ => const _PullRequests(),
    };
    return Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
      Padding(
        padding: const EdgeInsets.only(left: 16, right: 8),
        child: Row(children: [
          Expanded(
            child: Tabs(
              labels: tabs,
              index: index,
              badges: thread.kind == Kind.agent ? const {0: '3'} : const {},
              onChanged: (i) => studio.set(() => studio.inspectorTab = i),
            ),
          ),
          IconBtn(
            studio.inspectorWide ? CupertinoIcons.arrow_down_right_arrow_up_left : CupertinoIcons.arrow_up_left_arrow_down_right,
            tooltip: studio.inspectorWide ? 'Dock' : 'Expand for review',
            onTap: () => studio.set(() => studio.inspectorWide = !studio.inspectorWide),
          ),
        ]),
      ),
      const Hairline(),
      Expanded(child: body),
    ]);
  }
}

class _Scroll extends StatelessWidget {
  const _Scroll({required this.children, this.footer});
  final List<Widget> children;
  final Widget? footer;

  @override
  Widget build(BuildContext context) => Column(children: [
        Expanded(
          child: ListView(padding: const EdgeInsets.all(16), children: children),
        ),
        if (footer != null) ...[
          const Hairline(),
          Padding(padding: const EdgeInsets.all(12), child: footer),
        ],
      ]);
}

class _Check extends StatelessWidget {
  const _Check(this.label, this.detail, this.state);
  final String label;
  final String detail;

  /// 0 passed, 1 warning, 2 failed, 3 running.
  final int state;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    final color = [s.ok, s.warn, s.err, s.running][state];
    final icon = [
      CupertinoIcons.checkmark_circle_fill,
      CupertinoIcons.exclamationmark_triangle_fill,
      CupertinoIcons.xmark_circle_fill,
      CupertinoIcons.circle_fill,
    ][state];
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 5),
      child: Row(children: [
        Icon(icon, size: 12, color: color),
        const SizedBox(width: 9),
        Expanded(child: Text(label, style: s.text(12.5))),
        Text(detail, style: s.mono(11.5, color: s.muted)),
      ]),
    );
  }
}

class _Changes extends StatelessWidget {
  const _Changes({required this.thread});
  final Thread thread;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    const files = [
      ('internal/viewmodel/testdata/index.json', '+0', '−9'),
      ('internal/repo/testdata/old_symbols/', '+0', '−184'),
      ('internal/repo/symbols_test.go', '+3', '−11'),
    ];
    const diff = [
      (' ', '   "fixtures": ['),
      (' ', '     "turn_basic.json",'),
      ('-', '     "turn_legacy_v1.json",'),
      ('-', '     "turn_legacy_v2.json",'),
      (' ', '     "turn_tools.json",'),
      ('-', '     "stack_old_fold.json",'),
      (' ', '     "stack_fold.json"'),
      (' ', '   ]'),
    ];
    return _Scroll(
      footer: Row(children: [
        Expanded(child: Text('Gate passed with 1 warning', style: s.text(12, color: s.muted))),
        const Btn('Discard'),
        const SizedBox(width: 8),
        const Btn('Review & ship', primary: true),
      ]),
      children: [
        Label('Gate', trailing: Text('ran 2m ago', style: s.text(11.5, color: s.faint))),
        const SizedBox(height: 6),
        const _Check('Build', '4.1s', 0),
        const _Check('Tests', '412 passed', 0),
        const _Check('Lint', '1 warning', 1),
        const SizedBox(height: 16),
        Label('Files', count: files.length, trailing: Text('All · By step', style: s.text(11.5, color: s.muted))),
        const SizedBox(height: 6),
        for (var i = 0; i < files.length; i++)
          Container(
            padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 5),
            decoration: BoxDecoration(
              color: i == 0 ? s.selected : null,
              borderRadius: BorderRadius.circular(s.control),
            ),
            child: Row(children: [
              Expanded(child: Text(files[i].$1, overflow: TextOverflow.ellipsis, style: s.mono(11.5))),
              Text(files[i].$2, style: s.mono(11.5, color: s.ok)),
              const SizedBox(width: 6),
              Text(files[i].$3, style: s.mono(11.5, color: s.err)),
            ]),
          ),
        const SizedBox(height: 12),
        Container(
          decoration: BoxDecoration(
            border: Border.all(color: s.border),
            borderRadius: BorderRadius.circular(s.control),
          ),
          clipBehavior: Clip.antiAlias,
          child: Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
            for (final line in diff)
              Container(
                color: line.$1 == '-' ? s.err.withValues(alpha: 0.12) : null,
                padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 1),
                child: Text('${line.$1} ${line.$2}',
                    style: s.mono(11.5, color: line.$1 == '-' ? s.err : s.fg.withValues(alpha: 0.85))),
              ),
          ]),
        ),
        const SizedBox(height: 10),
        Text('Comment on a line to steer the agent. Its reply and new commits appear in the thread.',
            style: s.text(11.5, color: s.faint, height: 1.4)),
      ],
    );
  }
}

class _Files extends StatelessWidget {
  const _Files();

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    const tree = [
      (0, 'internal', true, false),
      (1, 'acp', true, false),
      (1, 'repo', true, false),
      (2, 'symbols.go', false, false),
      (2, 'symbols_test.go', false, true),
      (2, 'testdata', true, true),
      (1, 'viewmodel', true, false),
      (2, 'testdata', true, true),
      (3, 'index.json', false, true),
      (0, 'scripts', true, false),
      (0, 'AGENTS.md', false, false),
      (0, 'go.mod', false, false),
    ];
    return _Scroll(children: [
      for (final node in tree)
        Padding(
          padding: EdgeInsets.only(left: 14.0 * node.$1, top: 4, bottom: 4),
          child: Row(children: [
            Icon(node.$3 ? CupertinoIcons.folder : CupertinoIcons.doc, size: 12, color: s.muted),
            const SizedBox(width: 8),
            Expanded(child: Text(node.$2, style: s.mono(12))),
            if (node.$4)
              Tooltip(
                message: 'Changed in this thread',
                child: Container(width: 6, height: 6, decoration: BoxDecoration(color: s.warn, shape: BoxShape.circle)),
              ),
          ]),
        ),
    ]);
  }
}

class _Terminal extends StatelessWidget {
  const _Terminal();

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    const lines = [
      r'$ scripts/test.sh',
      'ok   marshal/internal/viewmodel   0.412s',
      'ok   marshal/internal/repo        1.207s',
      'ok   marshal/internal/acp         3.881s',
      '412 passed, 0 failed',
      r'$ ',
    ];
    return _Scroll(
      footer: Row(children: [
        Icon(CupertinoIcons.pause_circle, size: 13, color: s.muted),
        const SizedBox(width: 8),
        Expanded(
          child: Text('Typing here pauses the agent until you hand back.', style: s.text(12, color: s.muted)),
        ),
      ]),
      children: [
        Container(
          padding: const EdgeInsets.all(12),
          decoration: BoxDecoration(color: s.codeBg, borderRadius: BorderRadius.circular(s.control)),
          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            for (final line in lines)
              Text(line, style: s.mono(12, color: line.startsWith(r'$') ? s.fg : s.muted)),
          ]),
        ),
      ],
    );
  }
}

class _Preview extends StatelessWidget {
  const _Preview();

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    return Padding(
      padding: const EdgeInsets.all(16),
      child: Column(children: [
        Container(
          height: 30,
          padding: const EdgeInsets.symmetric(horizontal: 10),
          decoration: BoxDecoration(
            border: Border.all(color: s.border),
            borderRadius: BorderRadius.circular(s.control),
          ),
          child: Row(children: [
            Icon(CupertinoIcons.globe, size: 12, color: s.muted),
            const SizedBox(width: 8),
            Expanded(child: Text('port 5173  /bookings', style: s.mono(11.5, color: s.muted))),
            Icon(CupertinoIcons.arrow_clockwise, size: 12, color: s.muted),
          ]),
        ),
        const SizedBox(height: 10),
        Expanded(
          child: Container(
            alignment: Alignment.center,
            decoration: BoxDecoration(
              border: Border.all(color: s.border),
              borderRadius: BorderRadius.circular(s.control),
            ),
            child: Text('The agent\'s running app appears here,\nproxied from its declared ports.',
                textAlign: TextAlign.center, style: s.text(12.5, color: s.faint, height: 1.5)),
          ),
        ),
      ]),
    );
  }
}

class _Inspect extends StatelessWidget {
  const _Inspect();

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    const facts = [
      ('Step', '2 · Removed the fixtures that are plainly dead'),
      ('Tool', 'file.edit'),
      ('Why', 'Drop three index entries whose files no test loads'),
      ('Took', '0.3s'),
      ('Sandbox', 'container · network allowlist'),
      ('Approval', 'auto · matched "file.edit in worktree"'),
      ('Call', 'call_8f21c0'),
    ];
    return _Scroll(children: [
      Text('edit  internal/viewmodel/testdata/index.json', style: s.mono(12.5, weight: FontWeight.w600)),
      const SizedBox(height: 12),
      for (final f in facts)
        Padding(
          padding: const EdgeInsets.symmetric(vertical: 4),
          child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
            SizedBox(width: 72, child: Text(f.$1, style: s.text(12, color: s.muted))),
            Expanded(child: Text(f.$2, style: s.text(12.5, height: 1.4))),
          ]),
        ),
      const SizedBox(height: 14),
      Tabs(labels: const ['Output', 'Arguments', 'Thinking', 'Model request'], index: 0, onChanged: (_) {}),
      const Hairline(),
      const SizedBox(height: 10),
      Container(
        padding: const EdgeInsets.all(10),
        decoration: BoxDecoration(color: s.codeBg, borderRadius: BorderRadius.circular(s.control)),
        child: Text('Applied 1 edit. 9 lines removed.\nBackup kept; this step can be rolled back.',
            style: s.mono(11.5, color: s.muted)),
      ),
    ]);
  }
}

class _Plan extends StatelessWidget {
  const _Plan({required this.thread});
  final Thread thread;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    const scope = [
      ('Reconnect replay', 'PR #121 · in review', 2),
      ('Approval persistence', 'PR #124 · implementing', 1),
      ('Reattach handshake', 'waits on replay contract', 0),
      ('TUI reconnect screen', 'waits on handshake', 0),
      ('Docs and status output', 'not started', 0),
    ];
    return _Scroll(
      footer: Row(children: [
        Expanded(child: Text('Approved revision 2 · 1h 12m ago', style: s.text(12, color: s.muted))),
        const Btn('Propose a change'),
      ]),
      children: [
        Text('Goal', style: s.text(11.5, color: s.muted)),
        const SizedBox(height: 4),
        Text(
          'A session screen that loses its connection gets its session back, with nothing the user answered lost.',
          style: s.text(13, height: 1.5),
        ),
        const SizedBox(height: 16),
        const Label('Scope'),
        const SizedBox(height: 6),
        for (final item in scope)
          Padding(
            padding: const EdgeInsets.symmetric(vertical: 6),
            child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Padding(
                padding: const EdgeInsets.only(top: 2),
                child: Icon(
                  item.$3 == 2
                      ? CupertinoIcons.checkmark_circle_fill
                      : item.$3 == 1
                          ? CupertinoIcons.circle_fill
                          : CupertinoIcons.circle,
                  size: 12,
                  color: item.$3 == 2
                      ? s.ok
                      : item.$3 == 1
                          ? s.running
                          : s.faint,
                ),
              ),
              const SizedBox(width: 9),
              Expanded(
                child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                  Text(item.$1, style: s.text(13)),
                  Text(item.$2, style: s.text(11.5, color: s.muted)),
                ]),
              ),
            ]),
          ),
        const SizedBox(height: 16),
        const Label('Handoff settings'),
        const SizedBox(height: 6),
        for (final row in const [
          ('Implementer', 'sonnet · 2 in parallel'),
          ('Reviewers', 'correctness, security'),
          ('Repair allowance', '5 rounds'),
          ('Planner authority', 'answers agent questions'),
          ('Merge', 'by you'),
          ('Environment', 'go-service v4'),
        ])
          Padding(
            padding: const EdgeInsets.symmetric(vertical: 4),
            child: Row(children: [
              Expanded(child: Text(row.$1, style: s.text(12.5, color: s.muted))),
              Text(row.$2, style: s.text(12.5)),
            ]),
          ),
        const SizedBox(height: 16),
        const Label('Documents'),
        const SizedBox(height: 6),
        for (final doc in const ['Specification', 'Implementation plan', 'Planning conversation'])
          Padding(
            padding: const EdgeInsets.symmetric(vertical: 4),
            child: Row(children: [
              Icon(CupertinoIcons.doc_text, size: 12, color: s.muted),
              const SizedBox(width: 8),
              Text(doc, style: s.text(12.5)),
            ]),
          ),
      ],
    );
  }
}

class _Agents extends StatelessWidget {
  const _Agents();

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    final agents = [
      ('Planner', 'opus', 'Waiting on your decision', Status.needsYou, s.fg),
      ('Implementer A', 'sonnet', 'Idle · PR #121 repaired', Status.idle, s.fg),
      ('Implementer B', 'sonnet', 'Writing approval store tests', Status.running, s.fg),
      ('Reviewer', 'opus', 'Reviewing PR #121 head 3f1c2aa', Status.running, s.fg),
    ];
    return _Scroll(children: [
      for (final a in agents)
        Padding(
          padding: const EdgeInsets.only(bottom: 8),
          child: Box(
            padding: const EdgeInsets.all(12),
            onTap: () {},
            child: Row(children: [
              Dot(a.$4),
              const SizedBox(width: 10),
              Expanded(
                child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                  Row(children: [
                    Text(a.$1, style: s.text(13, color: a.$5, weight: FontWeight.w600)),
                    const SizedBox(width: 8),
                    Text(a.$2, style: s.mono(11, color: s.faint)),
                  ]),
                  const SizedBox(height: 2),
                  Text(a.$3, style: s.text(12, color: s.muted)),
                ]),
              ),
              Tooltip(message: 'Open its conversation', child: Icon(CupertinoIcons.chevron_right, size: 11, color: s.faint)),
            ]),
          ),
        ),
      const SizedBox(height: 4),
      Text('Open an agent to read its conversation in the centre and steer it directly.',
          style: s.text(11.5, color: s.faint, height: 1.4)),
    ]);
  }
}

class _Checks extends StatelessWidget {
  const _Checks();

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    return _Scroll(children: [
      Label('PR #121 · head 3f1c2aa', trailing: Text('current', style: s.text(11.5, color: s.ok))),
      const SizedBox(height: 6),
      const _Check('build (linux, cgo)', '2m 11s', 0),
      const _Check('test: go suite', '6m 40s', 0),
      const _Check('test: usability scripted', '3m 02s', 0),
      const _Check('web ui: check and build', '1m 18s', 0),
      const SizedBox(height: 16),
      const Label('PR #124 · head a09e771'),
      const SizedBox(height: 6),
      const _Check('build (linux, cgo)', '2m 09s', 0),
      const _Check('test: go suite', 'running', 3),
      const SizedBox(height: 16),
      const Label('PR #123 · head 77b0c1e'),
      const SizedBox(height: 6),
      const _Check('build (linux, cgo)', '2m 04s', 0),
      const _Check('test: usability scripted', 'failed', 2),
      Padding(
        padding: const EdgeInsets.only(left: 21, top: 4),
        child: Row(children: [
          Tooltip(
            message: 'A factory worker reproduces the failure, fixes it and pushes to this PR',
            child: Btn('Fix with a worker', primary: true, icon: CupertinoIcons.wrench),
          ),
          const SizedBox(width: 8),
          const Btn('Full log'),
        ]),
      ),
      const SizedBox(height: 16),
      const Label('Reviews'),
      const SizedBox(height: 6),
      const _Check('Correctness', '1 finding, repaired', 0),
      const _Check('Security', 'no findings', 0),
      const SizedBox(height: 16),
      const Label('Preview environments'),
      const SizedBox(height: 6),
      Text('None reported by the forge for these revisions.', style: s.text(12.5, color: s.muted)),
    ]);
  }
}

class _PullRequests extends StatelessWidget {
  const _PullRequests();

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    final prs = [
      ('#121', 'Reconnect replay', 'Draft · checks passed · 1 review round', s.ok),
      ('#124', 'Approval persistence', 'Draft · checks running', s.running),
    ];
    return _Scroll(children: [
      for (final pr in prs)
        Padding(
          padding: const EdgeInsets.only(bottom: 8),
          child: Box(
            padding: const EdgeInsets.all(12),
            onTap: () {},
            child: Row(children: [
              Text(pr.$1, style: s.mono(12, color: pr.$4, weight: FontWeight.w600)),
              const SizedBox(width: 10),
              Expanded(
                child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                  Text(pr.$2, style: s.text(13, weight: FontWeight.w500)),
                  Text(pr.$3, style: s.text(12, color: s.muted)),
                ]),
              ),
              Tooltip(message: 'Open on the forge', child: Icon(CupertinoIcons.arrow_up_right, size: 11, color: s.faint)),
            ]),
          ),
        ),
      const SizedBox(height: 4),
      Text('Each ready PR opens with CI first, then the engineer\'s narrated reasoning, then the code.',
          style: s.text(11.5, color: s.faint, height: 1.4)),
    ]);
  }
}
