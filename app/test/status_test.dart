import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/daemon/status.dart';

void main() {
  // The one example both languages parse: the Go test in cmd/marshal holds
  // `marshal daemon status --json` to this file, and this test reads it, so
  // a field either side renames fails on the side that changed.
  final fixture = File('../test/contract/daemon-status.json');

  test('parses the shared fixture', () {
    final json =
        jsonDecode(fixture.readAsStringSync()) as Map<String, dynamic>;
    final status = DaemonStatus.fromJson(json);

    expect(status.schemaVersion, DaemonStatus.supportedSchema);
    expect(status.answering, isTrue);
    expect(status.service.installed, isTrue);
    expect(status.service.running, isTrue);
    expect(status.service.pid, 1234);
    expect(status.linger.supported, isTrue);
    expect(status.linger.enabled, isTrue);

    final daemon = status.daemon;
    expect(daemon, isNotNull);
    expect(daemon!.version, '0.9.1');
    expect(daemon.projects, ['/home/u/src/marshal']);
    expect(daemon.children, hasLength(1));
    expect(daemon.children.single.root, '/home/u/src/marshal');
    expect(daemon.children.single.pinned, isTrue);
  });

  test('refuses an unknown schema', () {
    expect(
      () => DaemonStatus.fromJson(const {'schemaVersion': 2}),
      throwsA(isA<FormatException>()),
    );
  });

  test('tolerates a silent daemon', () {
    final status = DaemonStatus.fromJson(const {
      'schemaVersion': 1,
      'answering': false,
      'service': {'installed': true},
    });

    expect(status.answering, isFalse);
    expect(status.daemon, isNull);
    // Absent keys read as "not installed, not running, no pid" rather than
    // throwing: a daemon that did not answer cannot say more.
    expect(status.service.installed, isTrue);
    expect(status.service.running, isFalse);
    expect(status.service.pid, 0);
    expect(status.linger.supported, isFalse);
    expect(status.linger.enabled, isFalse);
  });
}
