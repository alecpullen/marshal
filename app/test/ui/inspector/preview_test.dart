import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/preview_model.dart';
import 'package:marshal_app/src/ui/inspector/preview_tab.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../../support/kit.dart';

PreviewModel _model({Iterable<int> ports = const [5173, 8080]}) =>
    PreviewModel.seeded(
      ports: ports,
      // The bridge issues the URL when the port is opened; a fixture holds
      // what it answered, so the tab can be tested without a bridge.
      urls: const {5173: 'http://127.0.0.1:5173/'},
    );

void main() {
  testWidgets('the declared ports are listed, and one opens to its URL', (
    tester,
  ) async {
    final model = _model();
    await pumpKit(
      tester,
      SizedBox(width: 400, height: 800, child: PreviewTab(model: model)),
      width: 500,
      height: 900,
    );
    await tester.pumpAndSettle();

    // Both ports are declared by the workspace, and named as ports.
    expect(find.text('port 5173'), findsOneWidget);
    expect(find.text('port 8080'), findsOneWidget);

    // Opening one shows the URL the bridge issued, with Copy link, and says
    // why it is not framed (decision 10).
    await tester.tap(find.text('port 5173'));
    await tester.pumpAndSettle();
    expect(find.text('http://127.0.0.1:5173/'), findsOneWidget);
    expect(find.widgetWithText(Btn, 'Copy link'), findsOneWidget);
    expect(find.textContaining('cannot show the agent'), findsOneWidget);
  });

  testWidgets('a workspace with no declared ports says so', (tester) async {
    await pumpKit(
      tester,
      SizedBox(
        width: 400,
        height: 800,
        child: PreviewTab(model: _model(ports: const [])),
      ),
      width: 500,
      height: 900,
    );
    await tester.pumpAndSettle();

    expect(find.byType(StateMessage), findsOneWidget);
    expect(find.textContaining('declares no preview ports'), findsOneWidget);
    expect(find.widgetWithText(Btn, 'Copy link'), findsNothing);
  });
}
