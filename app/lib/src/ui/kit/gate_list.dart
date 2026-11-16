import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'tip.dart';
import 'vocabulary.dart';

/// One check a gate ran: its name, what became of it, and one line of
/// detail — how long it took, or what it produced.
@immutable
class GateCheck {
  const GateCheck(this.name, this.state, {this.detail = ''});

  final String name;
  final CheckState state;

  /// The line beside the name. Mono: it is a duration or a count (§3).
  final String detail;
}

/// The gate's checks (design-system.md §5): one row per check, each with the
/// state's icon in the state's colour, the check's name, and its detail at
/// the right.
///
/// The order is the gate's own. The state is never colour alone: the icon's
/// shape says it, and the tooltip spells out the check and its state.
class GateList extends StatelessWidget {
  const GateList(this.checks, {super.key});

  final List<GateCheck> checks;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        for (final check in checks)
          Padding(
            padding: const EdgeInsets.symmetric(vertical: Space.s6),
            child: Row(
              children: [
                Tip(
                  message: '${check.name} · ${check.state.word.toLowerCase()}',
                  child: Semantics(
                    label: '${check.name}: ${check.state.word}',
                    child: Icon(
                      check.state.icon,
                      size: Sizes.iconRow,
                      color: check.state.color(e),
                    ),
                  ),
                ),
                const SizedBox(width: Space.s8),
                Expanded(
                  child: Text(
                    check.name,
                    style: e.body,
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                  ),
                ),
                if (check.detail.isNotEmpty) ...[
                  const SizedBox(width: Space.s8),
                  Text(
                    check.detail,
                    style: e.monoSmall.tint(e.muted),
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                  ),
                ],
              ],
            ),
          ),
      ],
    );
  }
}
