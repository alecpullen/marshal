import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import 'support/screens.dart';

/// design-system.md §7 and §11: each registered screen has a case for
/// loading, empty, stale, error and unavailable, and in each case it shows
/// that state with `StateMessage` — the only way these states are shown.
///
/// Loading, empty, error and unavailable replace the screen's content, so in
/// those cases no other state may be showing. Stale keeps the last data and
/// marks it, so it is only required to be there.
void main() {
  for (final screen in screens) {
    test('${screen.name} has a case for every state', () {
      final missing = requiredStates.difference(screen.states.keys.toSet());
      expect(
        missing,
        isEmpty,
        reason:
            '${screen.name} has no case for ${missing.map((s) => s.name)}; '
            'add it to test/support/screens.dart and handle it in the screen',
      );
    });

    for (final kind in requiredStates) {
      final build = screen.states[kind];
      if (build == null) continue;
      testWidgets('${screen.name} shows ${kind.name} with StateMessage', (
        tester,
      ) async {
        await tester.pumpWidget(
          EmberApp(brightness: Brightness.light, home: build()),
        );
        final shown = tester
            .widgetList<StateMessage>(find.byType(StateMessage))
            .map((m) => m.kind)
            .toList();
        expect(
          shown,
          contains(kind),
          reason:
              '${screen.name} in its ${kind.name} case shows no '
              'StateMessage.${kind.name}',
        );
        if (kind != StateKind.stale) {
          final others = shown
              .where((k) => k != kind && k != StateKind.planned)
              .toSet();
          expect(
            others,
            isEmpty,
            reason:
                '${screen.name} in its ${kind.name} case also shows '
                '${others.map((k) => k.name)}: is the case really the state?',
          );
        }
      });
    }
  }
}
