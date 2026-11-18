import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/fleet_profiles.dart';
import 'package:marshal_app/src/client/host.dart' show HostKind, HostReach;
import 'package:marshal_app/src/model/fleet_store.dart' show HostView;
import 'package:marshal_app/src/model/hosts_model.dart';

/// The `marshal fleet` commands as a test scripts them: every one is recorded
/// in the order it ran, and each can be made to fail.
class _Runner implements FleetProfiles {
  _Runner({this.supported = true});

  @override
  final bool supported;

  /// Every command, in the order it ran: "add studio", "login studio", …
  final calls = <String>[];

  /// What `add` answers with, when it fails.
  Object? addFailure;

  /// What `login` answers with, when it fails.
  Object? loginFailure;

  @override
  Future<void> add(String name, Uri url, {String? caFile}) async {
    calls.add('add $name');
    final failure = addFailure;
    if (failure != null) throw failure;
  }

  @override
  Future<void> login(String name, String token) async {
    calls.add('login $name');
    final failure = loginFailure;
    if (failure != null) throw failure;
  }

  @override
  Future<void> remove(String name) async {
    calls.add('remove $name');
  }
}

HostView _host(String name, {HostReach reach = HostReach.unreachable}) =>
    HostView(
      name: name,
      kind: HostKind.server,
      reach: reach,
      lastContact: DateTime.utc(2026, 11, 17, 4, 8),
    );

void main() {
  test('add runs add, login, then discovery', () async {
    final runner = _Runner();
    final model = HostsModel(
      () => const <HostView>[],
      profiles: runner,
      rediscover: () async => runner.calls.add('discover'),
    );

    await model.add(
      const HostDraft(
        name: 'studio',
        url: 'https://studio.example.com',
        token: 'a-token',
      ),
    );

    expect(runner.calls, ['add studio', 'login studio', 'discover']);
    expect(model.loginFailure('studio'), isNull);
  });

  test('a failed add stores nothing and throws its words', () async {
    final runner = _Runner()
      ..addFailure = const FleetProfileFailure(
        'studio is already in fleet.toml',
      );
    final model = HostsModel(
      () => const <HostView>[],
      profiles: runner,
      rediscover: () async => runner.calls.add('discover'),
    );

    await expectLater(
      model.add(
        const HostDraft(
          name: 'studio',
          url: 'https://studio.example.com',
          token: 'a-token',
        ),
      ),
      throwsA(
        isA<FleetProfileFailure>().having(
          (failure) => failure.message,
          'words',
          'studio is already in fleet.toml',
        ),
      ),
    );
    expect(runner.calls, ['add studio']);
    expect(model.loginFailure('studio'), isNull);
  });

  test('a failed login keeps the host added and says why', () async {
    final runner = _Runner()
      ..loginFailure = const FleetProfileFailure('the token was refused');
    final model = HostsModel(
      () => const <HostView>[],
      profiles: runner,
      rediscover: () async => runner.calls.add('discover'),
    );

    await model.add(
      const HostDraft(
        name: 'studio',
        url: 'https://studio.example.com',
        token: 'a-token',
      ),
    );

    expect(runner.calls, ['add studio', 'login studio', 'discover']);
    expect(model.loginFailure('studio'), 'the token was refused');
  });

  test('remove runs remove, then forgets', () async {
    final runner = _Runner();
    final forgotten = <String>[];
    final model = HostsModel(
      () => [_host('studio')],
      profiles: runner,
      forget: forgotten.add,
    );

    await model.remove('studio');

    expect(runner.calls, ['remove studio']);
    expect(forgotten, ['studio']);
  });

  test('sign in logs in, then retries', () async {
    final runner = _Runner()
      ..loginFailure = const FleetProfileFailure('the token was refused');
    final retried = <String>[];
    final model = HostsModel(
      () => const <HostView>[],
      profiles: runner,
      rediscover: () async => runner.calls.add('discover'),
      retry: retried.add,
    );
    await model.add(
      const HostDraft(
        name: 'studio',
        url: 'https://studio.example.com',
        token: 'a-stale-token',
      ),
    );
    expect(model.loginFailure('studio'), 'the token was refused');

    runner
      ..loginFailure = null
      ..calls.clear();
    await model.signIn('studio', 'a-fresh-token');

    expect(runner.calls, ['login studio']);
    expect(retried, ['studio']);
    expect(model.loginFailure('studio'), isNull);
  });

  test('a browser cannot add', () async {
    final runner = _Runner(supported: false);
    final model = HostsModel(() => const <HostView>[], profiles: runner);

    expect(model.canAdd, isFalse);
    await expectLater(
      model.add(
        const HostDraft(name: 'studio', url: 'https://studio.example.com'),
      ),
      throwsA(isA<FleetProfileFailure>()),
    );
    expect(runner.calls, isEmpty);
  });
}
