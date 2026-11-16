import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/inspect_model.dart';
import 'package:marshal_app/src/ui/inspector/inspect_tab.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../../support/kit.dart';

/// The node the fixture holds, as `InspectModel.seeded` takes it.
InspectModel _model() => InspectModel.seeded(
  facts: const InspectFacts(
    title: 'read internal/viewmodel/detail.go',
    kind: 'tool',
    actor: 'agent',
    why: 'Find where the fold test reads its fixture',
    took: '120 ms',
    sandbox: 'restricted · network isolated',
    approval: 'auto · read-only',
    callId: 'call_4d1e',
    output: 'Applied 1 read.',
    arguments: '{\n  "path": "internal/viewmodel/detail.go"\n}',
    thinking: ['The fold test loads a fixture beside it.'],
  ),
);

/// A model for a node that was selected: without an id the tab asks for one.
Future<InspectModel> _selected() async {
  final model = _model();
  // A seeded model reads nothing from a bridge, so selecting keeps the facts
  // it was given and only records which node is shown.
  await model.select('n-1');
  return model;
}

void main() {
  testWidgets('the node’s facts are listed, and each tab shows its own body', (
    tester,
  ) async {
    final model = await _selected();
    await pumpKit(
      tester,
      SizedBox(width: 400, height: 800, child: InspectTab(model: model)),
      width: 500,
      height: 900,
    );
    await tester.pumpAndSettle();

    // Decision 11's facts: actor, why, timing, sandbox, approval, call id.
    expect(find.text('read internal/viewmodel/detail.go'), findsOneWidget);
    expect(find.text('Actor'), findsOneWidget);
    expect(find.text('agent'), findsOneWidget);
    expect(
      find.text('Find where the fold test reads its fixture'),
      findsOneWidget,
    );
    expect(find.text('120 ms'), findsOneWidget);
    expect(find.text('restricted · network isolated'), findsOneWidget);
    expect(find.text('auto · read-only'), findsOneWidget);
    expect(find.text('call_4d1e'), findsOneWidget);

    // Output is the tab it opens on.
    expect(find.text('Applied 1 read.'), findsOneWidget);

    // Arguments is that call's own.
    await tester.tap(find.text('Arguments'));
    await tester.pumpAndSettle();
    expect(find.textContaining('internal/viewmodel/detail.go'), findsWidgets);
    expect(find.textContaining('"path"'), findsOneWidget);

    // Thinking is what the node recorded, not an empty panel.
    await tester.tap(find.text('Thinking'));
    await tester.pumpAndSettle();
    expect(
      find.text('The fold test loads a fixture beside it.'),
      findsOneWidget,
    );

    // The model request is read on demand: until it is, the tab says so.
    await tester.tap(find.text('Model request'));
    await tester.pumpAndSettle();
    expect(find.text('Open this tab to read the request.'), findsOneWidget);
  });

  testWidgets('the model request is shown once it has been read', (
    tester,
  ) async {
    final model = await _selected();
    // What the bridge answers when the request has been read.
    model.modelRequest = 'qwen2.5: 12 messages, 4 tool calls.';
    await pumpKit(
      tester,
      SizedBox(width: 400, height: 800, child: InspectTab(model: model)),
      width: 500,
      height: 900,
    );
    await tester.pumpAndSettle();

    await tester.tap(find.text('Model request'));
    await tester.pumpAndSettle();
    expect(find.text('qwen2.5: 12 messages, 4 tool calls.'), findsOneWidget);
  });

  testWidgets('with no node selected the tab asks for one', (tester) async {
    await pumpKit(
      tester,
      SizedBox(width: 400, height: 800, child: InspectTab(model: _model())),
      width: 500,
      height: 900,
    );
    await tester.pumpAndSettle();

    expect(find.byType(StateMessage), findsOneWidget);
    expect(find.text('Select a step to inspect it.'), findsOneWidget);
    expect(find.text('Actor'), findsNothing);
  });
}
