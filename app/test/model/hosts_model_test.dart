import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/fleet_profiles.dart';
import 'package:marshal_app/src/client/host.dart' show HostKind, HostReach;
import 'package:marshal_app/src/model/fleet_store.dart'
    show HostTunnel, HostView;
import 'package:marshal_app/src/model/hosts_model.dart';

/// The `marshal fleet` commands as a test scripts them: every one is recorded
/// in the order it ran, and each can be made to fail.
class _Runner implements FleetProfiles {
  _Runner({this.supported = true});

  @override
  final bool supported;

  /// Every command, in the order it ran: "add studio me@studio", "remove
  /// studio", …
  final calls = <String>[];

  /// What `add` answers with, when it fails.
  Object? addFailure;

  @override
  Future<void> add(String name, String destination) async {
    calls.add('add $name $destination');
    final failure = addFailure;
    if (failure != null) throw failure;
  }

  @override
  Future<void> remove(String name) async {
    calls.add('remove $name');
  }
}

HostView _host(
  String name, {
  HostReach reach = HostReach.unreachable,
  String? via,
  String? destination,
  HostTunnel? tunnel,
}) => HostView(
  name: name,
  kind: HostKind.server,
  reach: reach,
  lastContact: DateTime.utc(2026, 11, 17, 4, 8),
  via: via,
  destination: destination,
  tunnel: tunnel,
);

void main() {
  test('add runs add, then discovery', () async {
    final runner = _Runner();
    final model = HostsModel(
      () => const <HostView>[],
      profiles: runner,
      rediscover: () async => runner.calls.add('discover'),
    );

    await model.add(const HostDraft(name: 'studio', destination: 'me@studio'));

    expect(runner.calls, ['add studio me@studio', 'discover']);
  });

  test('add needs a name and a destination', () async {
    final runner = _Runner();
    final model = HostsModel(
      () => const <HostView>[],
      profiles: runner,
      rediscover: () async => runner.calls.add('discover'),
    );

    for (final draft in const [
      HostDraft(name: '', destination: 'me@studio'),
      HostDraft(name: 'studio', destination: ''),
      HostDraft(name: '  ', destination: '  '),
    ]) {
      await expectLater(
        model.add(draft),
        throwsA(
          isA<FleetProfileFailure>().having(
            (failure) => failure.message,
            'words',
            'A host needs a name and an SSH destination',
          ),
        ),
      );
    }
    expect(runner.calls, isEmpty, reason: 'nothing is written');
  });

  test('add trims both fields', () async {
    final runner = _Runner();
    final model = HostsModel(
      () => const <HostView>[],
      profiles: runner,
      rediscover: () async => runner.calls.add('discover'),
    );

    await model.add(
      const HostDraft(name: ' studio ', destination: ' me@studio '),
    );

    expect(runner.calls, ['add studio me@studio', 'discover']);
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
      model.add(const HostDraft(name: 'studio', destination: 'me@studio')),
      throwsA(
        isA<FleetProfileFailure>().having(
          (failure) => failure.message,
          'words',
          'studio is already in fleet.toml',
        ),
      ),
    );
    expect(runner.calls, ['add studio me@studio']);
  });

  test('fetch attaches each host’s tunnel', () async {
    final since = DateTime.utc(2026, 11, 17, 4, 0);
    final model = HostsModel(
      () => [_host('box', destination: 'me@box'), _host('studio')],
      tunnels: () async => {
        'box': HostTunnel(
          state: 'down',
          lastError: 'Permission denied (publickey).',
          since: since,
        ),
      },
    );

    await model.load();

    final box = model.items.first;
    expect(box.tunnel?.state, 'down');
    expect(box.tunnel?.lastError, 'Permission denied (publickey).');
    expect(box.tunnel?.since, since);
    expect(box.destination, 'me@box', reason: 'every other field is kept');
    expect(box.name, 'box');
    expect(model.items.last.tunnel, isNull, reason: 'no tunnel reported');
  });

  test('a tunnel reader that fails leaves the rows as they are', () async {
    final model = HostsModel(
      () => [_host('box', destination: 'me@box')],
      tunnels: () async =>
          throw const FleetProfileFailure('the daemon is not answering'),
    );

    await model.load();

    expect(model.items.single.name, 'box');
    expect(model.items.single.tunnel, isNull);
    expect(
      model.error,
      isNull,
      reason: 'a tunnel is an extra fact, not the list',
    );
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

  test('sign in stores through the callback, then retries', () async {
    // A browser keeps a host's token itself, so the sign-in is the callback's
    // write rather than a command (decision 6).
    final stored = <String>[];
    final retried = <String>[];
    final model = HostsModel(
      () => [_host('laptop', via: 'hub')],
      storeToken: (host, token) async => stored.add('$host: $token'),
      retry: retried.add,
    );

    await model.signIn('laptop', 'a-fresh-token');

    expect(stored, ['laptop: a-fresh-token']);
    expect(retried, ['laptop'], reason: 'a stored token is asked to try again');
  });

  test('a native build cannot sign in', () async {
    // The daemon reaches its hosts over its own tunnels, so a native build has
    // no token to store and nothing to sign in to.
    final model = HostsModel(() => [_host('studio')], profiles: _Runner());

    expect(model.canSignIn, isFalse);
    await expectLater(
      model.signIn('studio', 'a-token'),
      throwsA(
        isA<FleetProfileFailure>().having(
          (failure) => failure.message,
          'words',
          'This build cannot sign in to a host',
        ),
      ),
    );
  });

  test('sign out clears, then retries', () async {
    final cleared = <String>[];
    final retried = <String>[];
    final model = HostsModel.fixture(
      [_host('laptop', via: 'hub')],
      clearToken: cleared.add,
      retry: retried.add,
    );

    await model.signOut('laptop');

    // Cleared first, so the retry reads "token refused" again.
    expect(cleared, ['laptop']);
    expect(retried, ['laptop']);
  });

  test('only a relayed host can sign out', () async {
    final cleared = <String>[];
    final model = HostsModel.fixture(
      [_host('laptop', via: 'hub'), _host('studio')],
      profiles: _Runner(),
      clearToken: cleared.add,
    );

    expect(model.canSignOut(model.items.first), isTrue, reason: 'laptop');
    expect(
      model.canSignOut(model.items.last),
      isFalse,
      reason: 'a host reached directly carries the page’s own token',
    );

    // A native build keeps no token of its own: `marshal fleet token` reads one
    // when a request needs it, so there is nothing for it to clear.
    final native = HostsModel.fixture([
      _host('laptop', via: 'hub'),
    ], profiles: _Runner());
    expect(native.canSignOut(native.items.single), isFalse);
    await native.signOut('laptop');
    expect(cleared, isEmpty);
  });

  test('a browser cannot add', () async {
    final runner = _Runner(supported: false);
    final model = HostsModel(() => const <HostView>[], profiles: runner);

    expect(model.canAdd, isFalse);
    await expectLater(
      model.add(const HostDraft(name: 'studio', destination: 'me@studio')),
      throwsA(isA<FleetProfileFailure>()),
    );
    expect(runner.calls, isEmpty);
  });
}
