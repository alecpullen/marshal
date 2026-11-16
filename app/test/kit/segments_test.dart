import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();

  List<Color?> colours(WidgetTester tester) => [
    for (final d in decorationsUnder(tester, find.byType(Segments))) d.color,
  ];

  testWidgets('done is ok, current is the status, pending is border', (
    tester,
  ) async {
    await pumpKit(
      tester,
      const SizedBox(
        width: 300,
        child: Segments(done: 2, total: 5, status: Status.needsYou),
      ),
    );
    expect(colours(tester), [e.ok, e.ok, e.warn, e.border, e.border]);
  });

  testWidgets('running is fg; all done is all ok', (tester) async {
    await pumpKit(
      tester,
      const SizedBox(
        width: 300,
        child: Segments(done: 1, total: 3, status: Status.running),
      ),
    );
    expect(colours(tester), [e.ok, e.fg, e.border]);

    await pumpKit(
      tester,
      const SizedBox(
        width: 300,
        child: Segments(done: 4, total: 4, status: Status.ready),
      ),
    );
    expect(colours(tester), everyElement(e.ok));
  });

  testWidgets('its tooltip says how far along it is', (tester) async {
    await pumpKit(
      tester,
      const SizedBox(
        width: 300,
        child: Segments(done: 2, total: 5, status: Status.running),
      ),
    );
    expect(
      hasTip(tester, find.byType(Container).first, '2 of 5 steps done'),
      isTrue,
    );
  });
}
