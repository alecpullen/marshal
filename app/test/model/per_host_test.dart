import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/per_host.dart';

/// One value per host, built on first use and kept (decision 4): the sets of
/// models Automations, Library and Settings keep per host, and what forgetting
/// or disposing a host does to them.
void main() {
  test('a value is built once per host', () {
    var built = 0;
    final perHost = PerHost<String>((host) {
      built++;
      return 'value for $host';
    });

    expect(perHost.of('local'), 'value for local');
    expect(built, 1);
    expect(perHost.holds('local'), isTrue);
    expect(
      perHost.of('local'),
      'value for local',
      reason: 'the second read keeps the first build',
    );
    expect(
      built,
      1,
      reason: 'a build on every call would lose the rows it read',
    );
  });

  test('two hosts get two values', () {
    final built = <String>[];
    final perHost = PerHost<String>((host) {
      built.add(host);
      return 'value for $host';
    });

    expect(perHost.of('local'), 'value for local');
    expect(perHost.of('studio'), 'value for studio');
    expect(perHost.of('local'), 'value for local');
    expect(perHost.of('studio'), 'value for studio');

    expect(built, ['local', 'studio'], reason: 'one build per host, no more');
    expect(perHost.holds('local'), isTrue);
    expect(perHost.holds('studio'), isTrue);
  });

  test("forget disposes one host's value only", () {
    var built = 0;
    final disposed = <String>[];
    final perHost = PerHost<String>((host) {
      built++;
      return 'value for $host';
    }, dispose: disposed.add);
    perHost.of('local');
    perHost.of('studio');

    perHost.forget('local');

    expect(disposed, ['value for local']);
    expect(perHost.holds('local'), isFalse);
    expect(perHost.holds('studio'), isTrue);

    expect(perHost.of('local'), 'value for local');
    expect(perHost.of('studio'), 'value for studio');
    expect(
      built,
      3,
      reason: 'the forgotten host builds again, the kept one does not',
    );
    expect(disposed, ['value for local'], reason: 'nothing else was released');
  });

  test('dispose disposes every value', () {
    final disposed = <String>[];
    final perHost = PerHost<String>(
      (host) => 'value for $host',
      dispose: disposed.add,
    );
    perHost.of('local');
    perHost.of('studio');

    perHost.dispose();

    expect(disposed, ['value for local', 'value for studio']);
    expect(perHost.holds('local'), isFalse);
    expect(perHost.holds('studio'), isFalse);

    perHost.dispose();
    expect(disposed, [
      'value for local',
      'value for studio',
    ], reason: 'a second dispose releases nothing');
  });
}
