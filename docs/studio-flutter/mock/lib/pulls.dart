import 'package:cupertino_ui/cupertino_ui.dart' show CupertinoIcons;
import 'package:material_ui/material_ui.dart';

import 'data.dart';
import 'state.dart';
import 'theme.dart';
import 'widgets.dart';

Color _checkColor(S s, int state) => switch (state) {
      0 => s.ok,
      2 => s.err,
      3 => s.running,
      _ => s.border,
    };

IconData _checkIcon(int state) => switch (state) {
      0 => CupertinoIcons.checkmark_circle_fill,
      2 => CupertinoIcons.xmark_circle_fill,
      3 => CupertinoIcons.circle_fill,
      _ => CupertinoIcons.circle,
    };

String _checkSummary(Pull pull) {
  if (pull.failed) return '1 failing: ${pull.failing}';
  if (pull.running) return '${pull.passed} of ${pull.checks.length} checks passed, rest running';
  return '${pull.checks.length} of ${pull.checks.length} checks passed';
}

void _review(BuildContext context, Pull pull) {
  final studio = context.studio;
  studio.go(Place.thread, pull.thread);
  studio.set(() {
    studio.inspectorOpen = true;
    studio.inspectorWide = true;
    studio.inspectorTab = threadById(pull.thread).kind == Kind.agent ? 0 : 3;
  });
}

/// One block per CI check, in order, so a red one is visible at a glance.
class CheckStrip extends StatelessWidget {
  const CheckStrip(this.pull, {super.key});
  final Pull pull;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    return Row(mainAxisSize: MainAxisSize.min, children: [
      for (var i = 0; i < pull.checks.length; i++)
        Tooltip(
          message: checkNames[i],
          child: Container(
            width: 14,
            height: 6,
            margin: const EdgeInsets.only(right: 3),
            decoration: BoxDecoration(
              color: _checkColor(s, pull.checks[i]),
              borderRadius: BorderRadius.circular(2),
            ),
          ),
        ),
    ]);
  }
}

/// A pull request with its CI. The PR, not the thread, is the unit of review.
///
/// A failing PR carries the failure and one button. Sending it to a worker
/// needs no form: the project's defaults decide who fixes it and how far they
/// may go, and the caret holds the few things worth changing first.
class PrCard extends StatelessWidget {
  const PrCard({super.key, required this.pull});
  final Pull pull;

  @override
  Widget build(BuildContext context) {
    final studio = context.studio;
    final s = context.s;
    final thread = threadById(pull.thread);
    final project = projectById(thread.project);
    final fixing = studio.fixing.contains(pull.number);
    final failed = pull.failed && !fixing;
    final state = failed ? s.err : (pull.ready ? s.ok : s.running);

    void fix() => studio.set(() => studio.fixing.add(pull.number));

    return Box(
      edge: failed ? s.err.withValues(alpha: 0.55) : null,
      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Row(children: [
          Tooltip(
            message: failed
                ? 'Checks failing'
                : fixing
                    ? 'A worker is fixing the failure'
                    : pull.ready
                        ? 'Ready for review'
                        : 'In progress',
            child: Icon(
              failed
                  ? CupertinoIcons.xmark_circle_fill
                  : pull.ready
                      ? CupertinoIcons.checkmark_circle_fill
                      : CupertinoIcons.circle_fill,
              size: 13,
              color: state,
            ),
          ),
          const SizedBox(width: 8),
          Text('#${pull.number}', style: s.mono(12.5, color: s.muted)),
          const SizedBox(width: 8),
          Expanded(
            child: Text(pull.title, overflow: TextOverflow.ellipsis, style: s.text(14, weight: FontWeight.w600)),
          ),
          if (!pull.ready) ...[
            const Pill('Draft', tooltip: 'Not yet marked ready on the forge'),
            const SizedBox(width: 10),
          ],
          Tooltip(message: 'Last pushed', child: Text(pull.age, style: s.mono(11, color: s.faint))),
        ]),
        const SizedBox(height: 5),
        Row(children: [
          Flexible(
            child: Text('${project.repo}  ·  ${thread.branch} → main',
                overflow: TextOverflow.ellipsis, style: s.mono(11.5, color: s.muted)),
          ),
          const SizedBox(width: 10),
          Text('+${pull.adds}', style: s.mono(11.5, color: s.ok)),
          const SizedBox(width: 5),
          Text('−${pull.dels}', style: s.mono(11.5, color: s.err)),
        ]),
        const SizedBox(height: 10),
        Row(children: [
          CheckStrip(pull),
          const SizedBox(width: 8),
          Flexible(
            child: Text(_checkSummary(pull),
                overflow: TextOverflow.ellipsis,
                style: s.text(12.5, color: failed ? s.err : s.fg, weight: FontWeight.w500)),
          ),
          Text('  ·  ${pull.reviews}', style: s.text(12.5, color: s.muted)),
        ]),
        if (pull.failed) ...[
          const SizedBox(height: 10),
          Container(
            width: double.infinity,
            padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 8),
            decoration: BoxDecoration(color: s.codeBg, borderRadius: BorderRadius.circular(s.control)),
            child: Text(
              '--- FAIL: TestScriptedRecorderQuietMode (4.21s)\n'
              '    recorder_test.go:88: planner question interrupted a quiet capture\n'
              '    want 0 prompts while recording, got 1',
              style: s.mono(11.5, color: s.muted),
            ),
          ),
        ],
        const SizedBox(height: 10),
        if (fixing)
          Row(children: [
            const Dot(Status.running, tooltip: 'Worker running'),
            const SizedBox(width: 9),
            Expanded(
              child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                Text('A worker is on it', style: s.text(13, weight: FontWeight.w600)),
                const SizedBox(height: 2),
                Text(
                  'Reproduce, fix, run the gate, push to this PR. It comes back to you only if it cannot.',
                  style: s.text(12, color: s.muted),
                ),
              ]),
            ),
            const SizedBox(width: 10),
            Btn('Watch', onTap: () => studio.go(Place.thread, thread.id)),
            const SizedBox(width: 8),
            Btn('Stop', onTap: () => studio.set(() => studio.fixing.remove(pull.number))),
          ])
        else
          Row(children: [
            if (pull.ready) ...[
              Btn('Review', primary: true, onTap: () => _review(context, pull)),
              const SizedBox(width: 8),
              const Btn('Merge'),
              const SizedBox(width: 8),
            ] else if (pull.failed) ...[
              Tooltip(
                message: 'A factory worker reproduces the failure, fixes it and pushes to this PR',
                child: Btn('Fix with a worker', primary: true, icon: CupertinoIcons.wrench, onTap: fix),
              ),
              const SizedBox(width: 2),
              _FixOptions(project: project.name, onPick: fix),
              const SizedBox(width: 8),
              const Btn('Full log'),
              const SizedBox(width: 8),
            ],
            const Btn('Open on forge', icon: CupertinoIcons.arrow_up_right),
            const Spacer(),
            Tooltip(
              message: 'Open the ${kindLabel(thread.kind).toLowerCase()} this came from',
              child: Tap(
                onTap: () => studio.go(Place.thread, thread.id),
                builder: (context, hovered) => Row(mainAxisSize: MainAxisSize.min, children: [
                  Icon(kindIcon(thread.kind), size: 11, color: s.faint),
                  const SizedBox(width: 6),
                  Text(thread.title, style: s.text(12, color: hovered ? s.fg : s.muted)),
                ]),
              ),
            ),
          ]),
      ]),
    );
  }
}

/// The caret beside "Fix with a worker": the few things worth changing.
class _FixOptions extends StatelessWidget {
  const _FixOptions({required this.project, required this.onPick});
  final String project;
  final VoidCallback onPick;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    return PopupMenuButton<int>(
      tooltip: 'Other ways to send this',
      color: s.bg,
      elevation: 3,
      position: PopupMenuPosition.under,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(s.radius),
        side: BorderSide(color: s.border),
      ),
      onSelected: (_) => onPick(),
      itemBuilder: (context) => [
        for (final (i, item) in const [
          ('Investigate only', 'Report the cause, change nothing'),
          ('Fix with a note…', 'Tell the worker what you suspect'),
          ('Choose the worker…', 'Model, and how many attempts'),
          ('Always fix failures automatically', 'For every PR in this project'),
        ].indexed)
          PopupMenuItem(
            value: i,
            height: 46,
            child: Column(crossAxisAlignment: CrossAxisAlignment.start, mainAxisSize: MainAxisSize.min, children: [
              Text(i == 3 ? '${item.$1} in $project' : item.$1, style: s.text(12.5, weight: FontWeight.w500)),
              Text(item.$2, style: s.text(11.5, color: s.muted)),
            ]),
          ),
      ],
      child: Container(
        width: 26,
        height: 28,
        decoration: BoxDecoration(color: s.primary, borderRadius: BorderRadius.circular(s.control)),
        child: Icon(CupertinoIcons.chevron_down, size: 11, color: s.onPrimary),
      ),
    );
  }
}

/// Every open pull request Marshal produced, across projects, by CI state.
class PullsPage extends StatelessWidget {
  const PullsPage({super.key});

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    final ready = pulls.where((p) => p.ready).toList();
    final fixing = context.studio.fixing;
    final failing = pulls.where((p) => !p.ready && p.failed && !fixing.contains(p.number)).toList();
    final beingFixed = pulls.where((p) => fixing.contains(p.number)).toList();
    final progress = pulls.where((p) => !p.ready && !p.failed).toList();

    List<Widget> group(String title, List<Pull> items, Color? color) => [
          if (items.isNotEmpty) ...[
            Label(title, count: items.length, color: color),
            const SizedBox(height: 10),
            for (final p in items) Padding(padding: const EdgeInsets.only(bottom: 10), child: PrCard(pull: p)),
            const SizedBox(height: 14),
          ],
        ];

    return PageBody(
      title: 'Pull requests',
      subtitle: '${ready.length} ready for review · ${failing.length} with failing checks · ${progress.length} in progress',
      maxWidth: 860,
      children: [
        ...group('Ready for review', ready, s.ok),
        ...group('Checks failing', failing, s.err),
        ...group('Being fixed by a worker', beingFixed, null),
        ...group('In progress', progress, null),
      ],
    );
  }
}

/// The head of a ready thread: CI first, then the engineer's account of the
/// change, then the way into the code.
class ReadyReview extends StatelessWidget {
  const ReadyReview({super.key, required this.thread});
  final Thread thread;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    final mine = pulls.where((p) => p.thread == thread.id).toList();
    return Padding(
      padding: const EdgeInsets.only(bottom: 24),
      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Label('Ready for review', color: s.ok),
        const SizedBox(height: 10),
        for (final pull in mine)
          Box(
            padding: EdgeInsets.zero,
            child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Padding(
                padding: const EdgeInsets.fromLTRB(14, 12, 14, 10),
                child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                  Row(children: [
                    Text('#${pull.number}', style: s.mono(12.5, color: s.muted)),
                    const SizedBox(width: 8),
                    Expanded(child: Text(pull.title, style: s.text(15, weight: FontWeight.w600))),
                    Text('+${pull.adds}', style: s.mono(11.5, color: s.ok)),
                    const SizedBox(width: 5),
                    Text('−${pull.dels}', style: s.mono(11.5, color: s.err)),
                  ]),
                  const SizedBox(height: 4),
                  Text('${thread.branch} → main  ·  head 3f1c2aa  ·  checks ran on this head',
                      style: s.mono(11.5, color: s.muted)),
                ]),
              ),
              const Hairline(),
              Padding(
                padding: const EdgeInsets.fromLTRB(14, 10, 14, 10),
                child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                  Row(children: [
                    Text('Checks', style: s.text(12, color: s.muted)),
                    const SizedBox(width: 10),
                    CheckStrip(pull),
                    const SizedBox(width: 6),
                    Text(_checkSummary(pull), style: s.text(12.5, weight: FontWeight.w500)),
                  ]),
                  const SizedBox(height: 8),
                  Wrap(spacing: 18, runSpacing: 5, children: [
                    for (var i = 0; i < pull.checks.length; i++)
                      Row(mainAxisSize: MainAxisSize.min, children: [
                        Icon(_checkIcon(pull.checks[i]), size: 11, color: _checkColor(s, pull.checks[i])),
                        const SizedBox(width: 6),
                        Text(checkNames[i], style: s.text(12.5)),
                      ]),
                  ]),
                  const SizedBox(height: 8),
                  Row(children: [
                    Text('Reviews', style: s.text(12, color: s.muted)),
                    const SizedBox(width: 10),
                    Text(pull.reviews, style: s.text(12.5)),
                  ]),
                ]),
              ),
              const Hairline(),
              Padding(
                padding: const EdgeInsets.fromLTRB(14, 10, 14, 12),
                child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                  Text("Engineer's report", style: s.text(12, color: s.muted)),
                  const SizedBox(height: 5),
                  Text(
                    'The test failed about one run in twenty because it compared against the wall clock across a '
                    'midnight boundary. I pinned the clock in the fixture rather than widening the tolerance, so the '
                    'assertion still checks the exact slot. Ran the file 200 times locally with no failure.',
                    style: s.text(13.5, height: 1.55),
                  ),
                  const SizedBox(height: 12),
                  Row(children: [
                    Btn('Review changes', primary: true, onTap: () => _review(context, pull)),
                    const SizedBox(width: 8),
                    const Btn('Merge'),
                    const SizedBox(width: 8),
                    const Btn('Open on forge', icon: CupertinoIcons.arrow_up_right),
                    const Spacer(),
                    const Btn('Send back'),
                  ]),
                ]),
              ),
            ]),
          ),
      ]),
    );
  }
}
