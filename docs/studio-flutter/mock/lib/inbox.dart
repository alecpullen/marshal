import 'package:cupertino_ui/cupertino_ui.dart' show CupertinoIcons;
import 'package:material_ui/material_ui.dart';

import 'data.dart';
import 'pulls.dart';
import 'state.dart';
import 'theme.dart';
import 'widgets.dart';

IconData askIcon(Ask kind) => switch (kind) {
      Ask.decision => CupertinoIcons.arrow_branch,
      Ask.question => CupertinoIcons.question_circle,
      Ask.approval => CupertinoIcons.lock_shield,
      Ask.network => CupertinoIcons.globe,
      Ask.review => CupertinoIcons.text_bubble,
      Ask.intake => CupertinoIcons.tray_arrow_down,
    };

Color askColor(S s, Ask kind) => switch (kind) {
      Ask.review || Ask.intake => s.info,
      _ => s.warn,
    };

/// One thing waiting on the user. The same card is used on the Overview, in
/// the Inbox and pinned inside the thread it came from, so an answer given in
/// one place is the same action everywhere.
class AskCard extends StatelessWidget {
  const AskCard({super.key, required this.item, this.compact = false, this.showSource = true});
  final InboxItem item;
  final bool compact;
  final bool showSource;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    final thread = threadById(item.thread);
    final color = askColor(s, item.kind);
    return Box(
      edge: color.withValues(alpha: 0.55),
      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Row(children: [
          Icon(askIcon(item.kind), size: 13, color: color),
          const SizedBox(width: 7),
          Text(askLabel[item.kind]!, style: s.text(11.5, color: color, weight: FontWeight.w600)),
          if (showSource) ...[
            Text('  ·  ', style: s.text(11.5, color: s.faint)),
            Expanded(
              child: Tap(
                onTap: () => context.studio.go(Place.thread, thread.id),
                builder: (context, hovered) => Text(
                  '${projectById(thread.project).name} › ${thread.title}',
                  overflow: TextOverflow.ellipsis,
                  style: s.text(11.5, color: hovered ? s.fg : s.muted),
                ),
              ),
            ),
          ] else
            const Spacer(),
          const SizedBox(width: 10),
          Text('waiting ${item.waiting}', style: s.mono(11, color: s.faint)),
        ]),
        const SizedBox(height: 8),
        Text(item.title,
            style: item.kind == Ask.approval
                ? s.mono(13, weight: FontWeight.w600)
                : s.text(14, weight: FontWeight.w600)),
        if (!compact || item.kind == Ask.decision) ...[
          const SizedBox(height: 4),
          Text(item.body, style: s.text(12.5, color: s.muted, height: 1.45)),
        ],
        const SizedBox(height: 10),
        Wrap(spacing: 8, runSpacing: 8, children: [
          for (var i = 0; i < item.actions.length; i++) Btn(item.actions[i], primary: i == 0),
        ]),
      ]),
    );
  }
}

/// Everything waiting on the user, oldest first, then what is ready.
class Inbox extends StatefulWidget {
  const Inbox({super.key});

  @override
  State<Inbox> createState() => _InboxState();
}

class _InboxState extends State<Inbox> {
  int filter = 0;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    final blocking = inbox.where((i) => i.kind != Ask.review && i.kind != Ask.intake).toList();
    final other = inbox.where((i) => i.kind == Ask.review || i.kind == Ask.intake).toList();
    final ready = pulls.where((p) => p.ready).toList();
    final show = switch (filter) { 1 => blocking, 2 => other, _ => [...blocking, ...other] };

    return PageBody(
      title: 'Inbox',
      subtitle: '${blocking.length} blocking work · ${other.length} to look at · ${ready.length} ready for review',
      maxWidth: 820,
      children: [
        Tabs(
          labels: const ['All', 'Blocking', 'To look at', 'Ready'],
          badges: {0: '${inbox.length}', 1: '${blocking.length}', 2: '${other.length}', 3: '${ready.length}'},
          index: filter,
          onChanged: (i) => setState(() => filter = i),
        ),
        const Hairline(),
        const SizedBox(height: 16),
        if (filter != 3)
          for (final item in show)
            Padding(padding: const EdgeInsets.only(bottom: 10), child: AskCard(item: item)),
        if (filter == 0 || filter == 3) ...[
          if (filter == 0) ...[
            const SizedBox(height: 12),
            Label('Ready for review', count: ready.length, color: s.ok),
            const SizedBox(height: 10),
          ],
          for (final pull in ready)
            Padding(padding: const EdgeInsets.only(bottom: 10), child: PrCard(pull: pull)),
        ],
      ],
    );
  }
}
