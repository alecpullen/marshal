import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/host.dart';
import 'package:marshal_app/src/model/factory_store.dart';
import 'package:marshal_app/src/model/source_state.dart';

import '../support/fake_bridge.dart';
import 'support.dart';

/// A source's freshness is its own: what it last held, when it last answered,
/// and what did not arrive. One dead feed must not pass for an empty queue,
/// and it must not hide a healthy one.
void main() {
  /// A bridge that answers the factory list with a failure in the bridge's own
  /// words.
  void failFactory(ModelFixture fixture, String host) =>
      fixture.bridges[host]!.reply(
        'GET',
        '/api/factory/items',
        const FakeReply(
          500,
          headers: {'content-type': 'application/json'},
          body: '{"error":"offline"}',
        ),
      );

  /// The one row for [kind] on [host].
  SourceState row(
    List<SourceState> states, {
    SourceKind kind = SourceKind.factory,
    String host = 'local',
  }) => states.singleWhere((state) => state.kind == kind && state.host == host);

  test('a source that answered is healthy and says how long ago', () async {
    final fixture = await ModelFixture.start([
      const FixtureHost(name: 'local'),
    ]);
    final factory = FactoryStore(fixture.registry, now: fixture.clock.now);
    addTearDown(factory.dispose);
    await factory.refresh('local');
    fixture.clock.advance(const Duration(minutes: 3));

    final state = row(sourceStates(fixture.store, factory: factory));

    expect(state.healthy, isTrue);
    expect(state.stale, isFalse);
    expect(state.unavailable, isFalse);
    expect(state.connecting, isFalse);
    final words = state.words(fixture.clock.now());
    expect(words, contains('Factory: read'));
    expect(words, endsWith('ago'));
    expect(words, 'Factory: read 3m ago');
  });

  test('a failed read over a snapshot is stale, not empty', () async {
    final fixture = await ModelFixture.start([
      const FixtureHost(name: 'local'),
    ]);
    final factory = FactoryStore(fixture.registry, now: fixture.clock.now);
    addTearDown(factory.dispose);
    await factory.refresh('local');
    fixture.clock.advance(const Duration(minutes: 3));
    failFactory(fixture, 'local');
    await factory.refresh('local');

    final state = row(sourceStates(fixture.store, factory: factory));

    expect(state.loaded, isTrue);
    expect(state.stale, isTrue);
    expect(state.healthy, isFalse);
    expect(state.unavailable, isFalse);
    // The last read that worked is the age, not the failed one.
    expect(
      state.lastRead,
      fixture.clock.now().subtract(const Duration(minutes: 3)),
    );
    final words = state.words(fixture.clock.now());
    expect(words, contains('old, and the last read failed — offline'));
    expect(words, 'Factory: 3m old, and the last read failed — offline');
  });

  test('a source that never answered is unavailable, not empty', () async {
    final fixture = await ModelFixture.start([
      const FixtureHost(name: 'local'),
    ]);
    final factory = FactoryStore(fixture.registry, now: fixture.clock.now);
    addTearDown(factory.dispose);
    failFactory(fixture, 'local');
    await factory.refresh('local');

    final state = row(sourceStates(fixture.store, factory: factory));

    expect(state.loaded, isFalse);
    expect(state.unavailable, isTrue);
    expect(state.stale, isFalse);
    expect(state.healthy, isFalse);
    expect(state.connecting, isFalse);
    final words = state.words(fixture.clock.now());
    expect(words, contains('unavailable'));
    expect(words, 'Factory: unavailable — offline');
  });

  test(
    'a source making its first attempt is connecting, not unavailable',
    () async {
      final fixture = await ModelFixture.start([
        const FixtureHost(name: 'local'),
      ]);
      final factory = FactoryStore(fixture.registry, now: fixture.clock.now);
      addTearDown(factory.dispose);

      final state = row(sourceStates(fixture.store, factory: factory));

      expect(state.connecting, isTrue);
      expect(state.unavailable, isFalse);
      expect(state.stale, isFalse);
      expect(state.healthy, isFalse);
      expect(state.words(fixture.clock.now()), 'Factory: connecting');
    },
  );

  test('the rows are ordered by host then fleet before factory', () async {
    final fixture = await ModelFixture.start([
      const FixtureHost(name: 'local'),
      const FixtureHost(name: 'studio', kind: HostKind.server),
    ]);
    final factory = FactoryStore(fixture.registry, now: fixture.clock.now);
    addTearDown(factory.dispose);

    final states = sourceStates(fixture.store, factory: factory);

    expect(
      [for (final state in states) '${state.host}/${state.kind.name}'],
      ['local/fleet', 'local/factory', 'studio/fleet', 'studio/factory'],
    );
  });

  test('a factory that is not wired contributes no row', () async {
    final fixture = await ModelFixture.start([
      const FixtureHost(name: 'local'),
      const FixtureHost(name: 'studio', kind: HostKind.server),
    ]);

    final states = sourceStates(fixture.store);

    expect(states, hasLength(2));
    expect(
      [for (final state in states) state.kind],
      [SourceKind.fleet, SourceKind.fleet],
    );
    expect([for (final state in states) state.host], ['local', 'studio']);
  });
}
