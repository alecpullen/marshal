import 'package:cupertino_ui/cupertino_ui.dart' show CupertinoIcons;
import 'package:material_ui/material_ui.dart';

import 'composer.dart';
import 'data.dart';
import 'state.dart';
import 'widgets.dart';

/// A project's own page: its work as a board, and everything configured for it.
class ProjectPage extends StatefulWidget {
  const ProjectPage({super.key, required this.project});
  final Project project;

  @override
  State<ProjectPage> createState() => _ProjectPageState();
}

class _ProjectPageState extends State<ProjectPage> {
  int tab = 0;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    final project = widget.project;
    final mine = threads.where((t) => t.project == project.id).toList();
    final work = mine.where((t) => t.kind == Kind.work).toList();
    final other = mine.where((t) => t.kind != Kind.work).toList();

    return PageBody(
      title: project.name,
      subtitle: '${project.repo} · environment ${project.env} · CI ${project.ci}',
      actions: [
        Btn('New', primary: true, icon: CupertinoIcons.plus, onTap: () => showComposer(context)),
      ],
      children: [
        Tabs(
          labels: const ['Work', 'Automations', 'Defaults', 'Health'],
          index: tab,
          onChanged: (i) => setState(() => tab = i),
        ),
        const Hairline(),
        const SizedBox(height: 18),
        if (tab == 0) ...[
          Label('Factory', trailing: Text('Plan → implement → review → repair, up to a ready PR', style: s.text(11.5, color: s.muted))),
          const SizedBox(height: 10),
          LayoutBuilder(builder: (context, box) {
            final columns = [
              for (var i = 0; i < stages.length; i++)
                _Column(title: stages[i], items: work.where((t) => t.stage == i).toList()),
            ];
            if (box.maxWidth < 720) {
              return Column(children: [
                for (final c in columns) Padding(padding: const EdgeInsets.only(bottom: 10), child: c),
              ]);
            }
            return IntrinsicHeight(
              child: Row(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
                for (var i = 0; i < columns.length; i++) ...[
                  if (i > 0) const SizedBox(width: 10),
                  Expanded(child: columns[i]),
                ],
              ]),
            );
          }),
          const SizedBox(height: 24),
          Label('Agents and runs', count: other.length),
          const SizedBox(height: 10),
          Box(
            padding: EdgeInsets.zero,
            child: Column(children: [
              for (var i = 0; i < other.length; i++) ...[
                if (i > 0) const Hairline(),
                Tap(
                  onTap: () => context.studio.go(Place.thread, other[i].id),
                  builder: (context, hovered) => Container(
                    color: hovered ? s.hover : null,
                    padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 10),
                    child: Row(children: [
                      Dot(other[i].status),
                      const SizedBox(width: 10),
                      Icon(kindIcon(other[i].kind), size: 13, color: s.muted),
                      const SizedBox(width: 8),
                      Expanded(
                        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                          Text(other[i].title, style: s.text(13, weight: FontWeight.w500)),
                          Text(other[i].headline, style: s.text(12, color: s.muted)),
                        ]),
                      ),
                      Text(other[i].elapsed, style: s.mono(11, color: s.faint)),
                    ]),
                  ),
                ),
              ],
              if (other.isEmpty)
                Padding(
                  padding: const EdgeInsets.all(14),
                  child: Text('No independent agents or plan runs.', style: s.text(12.5, color: s.muted)),
                ),
            ]),
          ),
        ],
        if (tab == 1)
          const _Rows([
            ('PR review bot', 'Reviews every PR not opened by Marshal. Drafts land in the Inbox.', 'On · holds drafts'),
            ('CI fixer', 'Starts when a check fails on main and the failure reproduces.', 'On · 1 fix this week'),
            ('Issue intake', 'Issues labelled "marshal" become proposed agents.', 'Polling every 5m'),
            ('Schedule: dependency check', 'Runs the upgrade recipe nightly at 02:00.', 'Next in 9h'),
            ('Watch: gate duration', 'Notifies when the gate takes longer than 10 minutes.', 'Clear'),
          ]),
        if (tab == 2)
          const _Rows([
            ('Environment', 'Toolchains, packages, services, network rules and secrets.', 'go-service v4'),
            ('Agent default model', 'Used by independent agents started in this project.', 'qwen3.5-9b · local'),
            ('Factory roles', 'Planner, implementer and reviewers, each with a model.', 'opus · sonnet · opus'),
            ('Review focuses', 'What configured reviewers look for.', 'correctness, security'),
            ('Repair allowance', 'Review-and-fix rounds before a human is asked.', '5'),
            ('Planner authority', 'What the planner may decide without you.', 'Answers agent questions'),
            ('Ship target', 'Where finished work goes.', 'Draft PR to main'),
            ('Permission mode', 'Default for new agents.', 'Auto'),
          ]),
        if (tab == 3)
          const _Rows([
            ('Verify gate', 'Build, test and lint are runnable in the environment.', 'Runnable'),
            ('Repository mirror', 'Bare mirror on studio-box.', 'Fetched 2m ago'),
            ('Index', 'Embeddings and symbols, kept fresh while pinned.', 'Fresh · pinned'),
            ('Workspaces', 'Retained checkouts for this project.', '4 kept · 2.1 GB'),
            ('Forge connection', 'GitHub, fine-grained token.', 'Push and PRs allowed'),
            ('Discovery', 'What the repository already defines.', 'devcontainer, CI, AGENTS.md'),
          ]),
      ],
    );
  }
}

class _Column extends StatelessWidget {
  const _Column({required this.title, required this.items});
  final String title;
  final List<Thread> items;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    return Box(
      padding: const EdgeInsets.all(10),
      child: Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(4, 2, 4, 8),
          child: Row(children: [
            Text(title, style: s.text(12.5, weight: FontWeight.w600)),
            const SizedBox(width: 6),
            Text('${items.length}', style: s.mono(11.5, color: s.faint)),
          ]),
        ),
        for (final t in items)
          Padding(
            padding: const EdgeInsets.only(bottom: 8),
            child: Box(
              padding: const EdgeInsets.all(10),
              edge: t.status == Status.needsYou ? s.warn.withValues(alpha: 0.55) : null,
              onTap: () => context.studio.go(Place.thread, t.id),
              child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                Row(children: [
                  Dot(t.status, size: 7),
                  const SizedBox(width: 8),
                  Expanded(
                    child: Text(t.title, style: s.text(13, weight: FontWeight.w500)),
                  ),
                ]),
                const SizedBox(height: 4),
                Text(t.headline, style: s.text(12, color: s.muted, height: 1.35)),
                const SizedBox(height: 8),
                Segments(done: t.done, total: t.total, status: t.status),
              ]),
            ),
          ),
        if (items.isEmpty)
          Padding(
            padding: const EdgeInsets.fromLTRB(4, 0, 4, 6),
            child: Text('Nothing here', style: s.text(12, color: s.faint)),
          ),
      ]),
    );
  }
}

/// A plain settings-style list: name, what it is, current value.
class _Rows extends StatelessWidget {
  const _Rows(this.rows);
  final List<(String, String, String)> rows;

  @override
  Widget build(BuildContext context) => SettingRows(rows);
}

class SettingRows extends StatelessWidget {
  const SettingRows(this.rows, {super.key});
  final List<(String, String, String)> rows;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    return Box(
      padding: EdgeInsets.zero,
      child: Column(children: [
        for (var i = 0; i < rows.length; i++) ...[
          if (i > 0) const Hairline(),
          Tap(
            onTap: () {},
            builder: (context, hovered) => Container(
              color: hovered ? s.hover : null,
              padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 11),
              child: Row(children: [
                Expanded(
                  child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                    Text(rows[i].$1, style: s.text(13, weight: FontWeight.w500)),
                    const SizedBox(height: 2),
                    Text(rows[i].$2, style: s.text(12, color: s.muted)),
                  ]),
                ),
                const SizedBox(width: 12),
                Text(rows[i].$3, style: s.text(12.5, color: s.muted)),
                const SizedBox(width: 8),
                Icon(CupertinoIcons.chevron_right, size: 10, color: s.faint),
              ]),
            ),
          ),
        ],
      ]),
    );
  }
}
