import 'package:cupertino_ui/cupertino_ui.dart' show CupertinoIcons;
import 'package:material_ui/material_ui.dart';

import 'composer.dart';
import 'data.dart';
import 'inbox.dart';
import 'pulls.dart';
import 'state.dart';
import 'widgets.dart';

/// The dashboard. It answers four questions in order: what needs me, what is
/// running, what is ready, and is the fleet healthy.
class Overview extends StatelessWidget {
  const Overview({super.key});

  @override
  Widget build(BuildContext context) {
    final studio = context.studio;
    final s = context.s;
    final running = threads.where((t) => t.status == Status.running).toList();

    return LayoutBuilder(builder: (context, box) {
      final wide = box.maxWidth > 1020;

      final main = Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Label('Needs you', count: inbox.length, color: s.warn, trailing: _More('Open inbox', () => studio.go(Place.inbox))),
        const SizedBox(height: 10),
        for (final item in inbox.take(3))
          Padding(padding: const EdgeInsets.only(bottom: 8), child: AskCard(item: item, compact: true)),
        const SizedBox(height: 14),
        Label('Pull requests', count: pulls.length, trailing: _More('All pull requests', () => studio.go(Place.pulls))),
        const SizedBox(height: 10),
        for (final pull in pulls.where((p) => p.ready || p.failed))
          Padding(padding: const EdgeInsets.only(bottom: 8), child: PrCard(pull: pull)),
        Padding(
          padding: const EdgeInsets.only(left: 2, top: 2),
          child: Text('${pulls.where((p) => !p.ready && !p.failed).length} more in progress with checks passing or running',
              style: s.text(12, color: s.muted)),
        ),
        const SizedBox(height: 22),
        Label('Running now', count: running.length),
        const SizedBox(height: 10),
        _Grid(
          minTile: 260,
          children: [for (final t in running) _LiveTile(thread: t)],
        ),
        const SizedBox(height: 22),
        const Label('Projects'),
        const SizedBox(height: 10),
        const _ProjectsTable(),
      ]);

      final side = Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        const Label('Capacity'),
        const SizedBox(height: 10),
        Box(
          child: Column(children: [
            const Meter('studio-box', '6 of 8 slots', 0.75),
            const SizedBox(height: 12),
            const Meter('Local daemon', '1 of 2 slots', 0.5),
            const SizedBox(height: 12),
            Meter('vps-syd', 'unreachable 4m', 0, color: s.err),
            const SizedBox(height: 12),
            Meter('Disk', '41 of 120 GB', 0.34, color: s.muted),
          ]),
        ),
        const SizedBox(height: 22),
        const Label('Spend today'),
        const SizedBox(height: 10),
        Box(
          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            Row(crossAxisAlignment: CrossAxisAlignment.end, children: [
              Text(r'$5.92', style: s.text(22, weight: FontWeight.w600)),
              const SizedBox(width: 6),
              Padding(
                padding: const EdgeInsets.only(bottom: 3),
                child: Text(r'of $20 cap', style: s.text(12, color: s.muted)),
              ),
            ]),
            const SizedBox(height: 8),
            const Spark([0.2, 0.3, 0.3, 0.6, 0.9, 1.4, 1.5, 2.2, 3.1, 3.4, 4.6, 5.1, 5.9]),
            const SizedBox(height: 8),
            Text('Implementer 61% · reviewer 24% · planner 15%', style: s.text(11.5, color: s.muted)),
          ]),
        ),
        const SizedBox(height: 22),
        const Label('Automations'),
        const SizedBox(height: 10),
        Box(
          padding: EdgeInsets.zero,
          child: Column(children: [
            _SideRow(CupertinoIcons.exclamationmark_triangle, 'Watch tripped: local inference slow', '6m', s.warn),
            const Hairline(),
            _SideRow(CupertinoIcons.wrench, 'CI fixer opened PR #122 on av-portal', '31m', s.ok),
            const Hairline(),
            _SideRow(CupertinoIcons.clock, 'Nightly dependency check runs at 02:00', '', s.muted),
          ]),
        ),
        const SizedBox(height: 22),
        const Label('Activity'),
        const SizedBox(height: 10),
        for (final line in const [
          ('Voice planning', 'pushed draft PR #123', '4m'),
          ('Fixture cleanup', 'asked a question', '8m'),
          ('CI-first change review', 'became ready', '26m'),
          ('P6 plan 4', 'committed task 2', '33m'),
        ])
          Padding(
            padding: const EdgeInsets.only(bottom: 7),
            child: Row(children: [
              Expanded(
                child: Text.rich(
                  TextSpan(children: [
                    TextSpan(text: line.$1, style: s.text(12.5, weight: FontWeight.w500)),
                    TextSpan(text: '  ${line.$2}', style: s.text(12.5, color: s.muted)),
                  ]),
                  overflow: TextOverflow.ellipsis,
                ),
              ),
              Text(line.$3, style: s.mono(11, color: s.faint)),
            ]),
          ),
      ]);

      return PageBody(
        title: 'Overview',
        subtitle: '8 threads across 3 projects · 4 running · 2 of 3 hosts online',
        actions: [
          Btn('New', primary: true, icon: CupertinoIcons.plus, onTap: () => showComposer(context)),
        ],
        children: [
          if (wide)
            Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Expanded(child: main),
              const SizedBox(width: 28),
              SizedBox(width: 300, child: side),
            ])
          else ...[main, const SizedBox(height: 22), side],
        ],
      );
    });
  }
}

class _More extends StatelessWidget {
  const _More(this.label, this.onTap);
  final String label;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    return Tap(
      onTap: onTap,
      builder: (context, hovered) => Text('$label →', style: s.text(12, color: hovered ? s.fg : s.muted)),
    );
  }
}

/// Lays children out in equal columns, as many as fit at `minTile` wide.
class _Grid extends StatelessWidget {
  const _Grid({required this.children, required this.minTile});
  final List<Widget> children;
  final double minTile;

  @override
  Widget build(BuildContext context) {
    return LayoutBuilder(builder: (context, box) {
      const gap = 10.0;
      // Balance the rows: four tiles in a three-column space become two by two.
      final fit = (box.maxWidth / minTile).floor().clamp(1, 4);
      final rows = (children.length / fit).ceil().clamp(1, 99);
      final columns = (children.length / rows).ceil().clamp(1, fit);
      final width = (box.maxWidth - gap * (columns - 1)) / columns;
      return Wrap(
        spacing: gap,
        runSpacing: gap,
        children: [for (final child in children) SizedBox(width: width, child: child)],
      );
    });
  }
}

class _LiveTile extends StatelessWidget {
  const _LiveTile({required this.thread});
  final Thread thread;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    return Box(
      onTap: () => context.studio.go(Place.thread, thread.id),
      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Row(children: [
          Dot(thread.status),
          const SizedBox(width: 8),
          Expanded(
            child: Text(thread.title, overflow: TextOverflow.ellipsis, style: s.text(13, weight: FontWeight.w600)),
          ),
          Text(thread.elapsed, style: s.mono(11, color: s.faint)),
        ]),
        const SizedBox(height: 3),
        Text('${projectById(thread.project).name} · ${kindLabel(thread.kind).toLowerCase()} · ${thread.host}',
            overflow: TextOverflow.ellipsis, style: s.text(11.5, color: s.muted)),
        const SizedBox(height: 10),
        Segments(done: thread.done, total: thread.total, status: thread.status),
        const SizedBox(height: 10),
        for (var i = 0; i < thread.recent.length; i++)
          Padding(
            padding: const EdgeInsets.only(bottom: 3),
            child: Text(
              thread.recent[i],
              overflow: TextOverflow.ellipsis,
              style: s.text(12, color: i == thread.recent.length - 1 ? s.fg : s.muted),
            ),
          ),
      ]),
    );
  }
}

class _ProjectsTable extends StatelessWidget {
  const _ProjectsTable();

  @override
  Widget build(BuildContext context) {
    final s = context.s;

    Widget cell(String text, {int flex = 1, Color? color, bool head = false, bool mono = false}) => Expanded(
          flex: flex,
          child: Text(
            text,
            overflow: TextOverflow.ellipsis,
            style: head
                ? s.text(11.5, color: s.muted)
                : mono
                    ? s.mono(12, color: color ?? s.fg)
                    : s.text(12.5, color: color ?? s.fg),
          ),
        );

    int count(String project, Status status) =>
        threads.where((t) => t.project == project && t.status == status).length;

    return Box(
      padding: EdgeInsets.zero,
      child: Column(children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(14, 9, 14, 7),
          child: Row(children: [
            cell('Project', flex: 3, head: true),
            cell('Running', head: true),
            cell('Needs you', head: true),
            cell('Ready', head: true),
            cell('CI', flex: 2, head: true),
            cell('Today', head: true),
          ]),
        ),
        for (final p in projects) ...[
          const Hairline(),
          Tap(
            onTap: () => context.studio.go(Place.project, p.id),
            builder: (context, hovered) => Container(
              color: hovered ? s.hover : null,
              padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 9),
              child: Row(children: [
                Expanded(
                  flex: 3,
                  child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                    Text(p.name, style: s.text(13, weight: FontWeight.w500)),
                    Text(p.repo, overflow: TextOverflow.ellipsis, style: s.text(11.5, color: s.muted)),
                  ]),
                ),
                cell('${count(p.id, Status.running)}', mono: true),
                cell('${count(p.id, Status.needsYou)}',
                    mono: true, color: count(p.id, Status.needsYou) > 0 ? s.warn : s.faint),
                cell('${count(p.id, Status.ready)}', mono: true, color: count(p.id, Status.ready) > 0 ? s.ok : s.faint),
                cell(p.ci, flex: 2, color: p.ci == 'passing' ? s.muted : s.err),
                cell(p.spend, mono: true, color: s.muted),
              ]),
            ),
          ),
        ],
      ]),
    );
  }
}

class _SideRow extends StatelessWidget {
  const _SideRow(this.icon, this.text, this.when, this.color);
  final IconData icon;
  final String text;
  final String when;
  final Color color;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 9),
      child: Row(children: [
        Icon(icon, size: 13, color: color),
        const SizedBox(width: 9),
        Expanded(child: Text(text, style: s.text(12.5))),
        if (when.isNotEmpty) Text(when, style: s.mono(11, color: s.faint)),
      ]),
    );
  }
}
