import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/fleet_profiles.dart';
import 'package:marshal_app/src/client/host.dart' show HostKind, HostReach;
import 'package:marshal_app/src/model/fleet_store.dart'
    show HostTunnel, HostView;
import 'package:marshal_app/src/model/hosts_model.dart';

/// The `marshal fleet` commands as a test scripts them: every one is recorded
/// in the order it ran, and each can be made to fail.
class _Runner implements FleetProfiles {
  @override
  bool get supported => true;

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

  test('a model without profiles cannot add', () async {
    final model = HostsModel(() => const <HostView>[]);

    expect(model.canAdd, isFalse);
    await expectLater(
      model.add(const HostDraft(name: 'studio', destination: 'me@studio')),
      throwsA(isA<FleetProfileFailure>()),
    );
  });
}
