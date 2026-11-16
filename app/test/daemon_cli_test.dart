import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/daemon/cli.dart';

/// The menu's `marshal not found` state hangs off DaemonCli telling a
/// spawn's missing binary apart from every other failure, so that
/// classification is pinned here the way the menu test pins its label.
void main() {
  test('a missing binary raises MarshalMissingException, not the raw error',
      () async {
    await expectLater(
      DaemonCli(binary: '/nonexistent/marshal').status(),
      throwsA(
        isA<MarshalMissingException>().having(
          (error) => error.toString(),
          'message',
          'marshal not found',
        ),
      ),
    );
  });
}
