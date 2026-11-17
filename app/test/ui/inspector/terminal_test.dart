import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/terminal_session.dart';
import 'package:marshal_app/src/ui/inspector/terminal_tab.dart';

import '../../support/kit.dart';

void main() {
  testWidgets('a shell that never started shows the bridge’s reason', (
    tester,
  ) async {
    // The bridge attaches the failure to the chunk that ends the stream; the
    // tab draws it rather than an empty terminal (decision 9).
    final session = TerminalSession.seeded(
      shellError: 'the shell could not be started: no pty',
    );
    await pumpKit(tester, TerminalTab(session: session));

    expect(find.textContaining('no pty'), findsOneWidget);
    expect(find.text('Try again'), findsOneWidget);
  });

  testWidgets('a shell that is running draws the terminal and the pause rule', (
    tester,
  ) async {
    final session = TerminalSession.seeded();
    await pumpKit(tester, TerminalTab(session: session));

    expect(find.textContaining('Typing here pauses the agent'), findsOneWidget);
    expect(find.textContaining('no pty'), findsNothing);
  });
}
