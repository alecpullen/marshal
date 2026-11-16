import 'package:cupertino_ui/cupertino_ui.dart' show CupertinoIcons;
import 'package:material_ui/material_ui.dart';

import 'state.dart';
import 'widgets.dart';

/// One way to start anything. The prompt comes first; the three kinds of
/// thread are a choice made after you know what you want, not three pages.
void showComposer(BuildContext context) {
  final studio = context.studio;
  showDialog<void>(
    context: context,
    barrierColor: Colors.black.withValues(alpha: 0.35),
    builder: (_) => StudioScope(state: studio, child: const _ComposerDialog()),
  );
}

class _ComposerDialog extends StatefulWidget {
  const _ComposerDialog();

  @override
  State<_ComposerDialog> createState() => _ComposerDialogState();
}

class _ComposerDialogState extends State<_ComposerDialog> {
  int kind = 0;
  int tab = 0;

  static const kinds = [
    ('Agent', 'One agent starts on this now. You steer it in conversation.'),
    ('Plan first', 'Talk it through with a planner. Decide later whether it becomes agents or a work item.'),
    ('Work item', 'Hand the plan to the project factory: implement, review and repair up to a ready PR.'),
  ];

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    return Dialog(
      backgroundColor: s.bg,
      elevation: 12,
      insetPadding: const EdgeInsets.all(20),
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(s.radius + 2),
        side: BorderSide(color: s.border),
      ),
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 680, maxHeight: 620),
        child: DefaultTextStyle(
          style: s.text(13),
          child: Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.stretch, children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(18, 14, 18, 0),
              child: TextField(
                autofocus: true,
                minLines: 3,
                maxLines: 8,
                style: s.text(15, height: 1.45),
                cursorColor: s.accent,
                decoration: InputDecoration(
                  hintText: 'What do you want done?',
                  hintStyle: s.text(15, color: s.faint),
                  border: InputBorder.none,
                  isDense: true,
                ),
              ),
            ),
            Padding(
              padding: const EdgeInsets.fromLTRB(18, 4, 18, 12),
              child: Wrap(spacing: 6, runSpacing: 6, children: [
                const Pill('marshal', icon: CupertinoIcons.folder, tooltip: 'Project'),
                const Pill('main', icon: CupertinoIcons.arrow_branch, mono: true, tooltip: 'Base branch'),
                const Pill('go-service', icon: CupertinoIcons.cube, tooltip: 'Environment the agent runs in'),
                const Pill('qwen3.5-9b · local', icon: CupertinoIcons.sparkles, tooltip: 'Model'),
                Pill(kind == 2 ? 'Factory authority' : 'Auto', icon: CupertinoIcons.shield, tooltip: 'Permission mode'),
                const Pill('New workspace', icon: CupertinoIcons.square_on_square, tooltip: 'Fresh isolated checkout, or resume one'),
                const Pill('Any host', icon: CupertinoIcons.desktopcomputer, tooltip: 'Where it runs'),
              ]),
            ),
            const Hairline(),
            Padding(
              padding: const EdgeInsets.fromLTRB(18, 12, 18, 12),
              child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
                for (var i = 0; i < kinds.length; i++) ...[
                  if (i > 0) const SizedBox(width: 8),
                  Expanded(
                    child: Box(
                      padding: const EdgeInsets.all(10),
                      edge: i == kind ? s.accent : null,
                      onTap: () => setState(() => kind = i),
                      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                        Text(kinds[i].$1,
                            style: s.text(13, weight: FontWeight.w600, color: i == kind ? s.accent : s.fg)),
                        const SizedBox(height: 3),
                        Text(kinds[i].$2, style: s.text(11.5, color: s.muted, height: 1.4)),
                      ]),
                    ),
                  ),
                ],
              ]),
            ),
            const Hairline(),
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 18),
              child: Tabs(
                labels: const ['Issues', 'Recent prompts', 'Recipes'],
                index: tab,
                onChanged: (i) => setState(() => tab = i),
              ),
            ),
            const Hairline(),
            Flexible(
              child: ListView(
                shrinkWrap: true,
                padding: const EdgeInsets.symmetric(horizontal: 18, vertical: 8),
                children: [
                  for (final row in const [
                    [
                      ('#131', 'daemon pin should accept --json'),
                      ('#127', 'Fleet screen loses selection after reconnect'),
                      ('#119', 'Usage tab double-counts subtask turns'),
                    ],
                    [
                      ('8m', 'Clean up test fixtures nobody uses any more'),
                      ('2d', 'Find why snapshot capture takes 35 seconds'),
                      ('3d', 'Write the P6 plan 4 from the approved spec'),
                    ],
                    [
                      ('recipe', 'Review a pull request (read-only)'),
                      ('recipe', 'Fix a failing CI check'),
                      ('recipe', 'Upgrade dependencies and run the gate'),
                    ],
                  ][tab])
                    Padding(
                      padding: const EdgeInsets.symmetric(vertical: 6),
                      child: Row(children: [
                        SizedBox(width: 52, child: Text(row.$1, style: s.mono(11.5, color: s.faint))),
                        Expanded(child: Text(row.$2, style: s.text(13))),
                      ]),
                    ),
                ],
              ),
            ),
            const Hairline(),
            Padding(
              padding: const EdgeInsets.all(12),
              child: Row(children: [
                const IconBtn(CupertinoIcons.mic, tooltip: 'Talk it through instead'),
                const IconBtn(CupertinoIcons.paperclip, tooltip: 'Attach'),
                const Spacer(),
                Btn('Cancel', onTap: () => Navigator.of(context).pop()),
                const SizedBox(width: 8),
                Btn(
                  const ['Start agent', 'Start planning', 'Review handoff'][kind],
                  primary: true,
                  onTap: () => Navigator.of(context).pop(),
                ),
              ]),
            ),
          ]),
        ),
      ),
    );
  }
}
