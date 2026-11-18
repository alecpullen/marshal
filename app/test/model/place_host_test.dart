import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/host.dart' show HostKind, HostReach;
import 'package:marshal_app/src/model/fleet_store.dart' show HostView;
import 'package:marshal_app/src/model/place_host.dart';

/// The host a per-host place reads (spec D2): the scope when it names a host
/// this fleet knows, and "All hosts" answered by the first host that can
/// answer at all — so opening Automations, Library or Settings under it reads
/// something rather than nothing.
void main() {
  HostView host(String name, HostReach reach) => HostView(
    name: name,
    kind: HostKind.local,
    reach: reach,
    lastContact: null,
  );

  test('the scope wins when it names a known host', () {
    final hosts = [
      host('local', HostReach.live),
      host('studio', HostReach.unreachable),
    ];

    expect(placeHost(hosts, 'studio'), 'studio');
    expect(placeHost(hosts, 'local'), 'local');
  });

  test('all hosts reads the first live host', () {
    final hosts = [
      host('studio', HostReach.unreachable),
      host('local', HostReach.live),
      host('build', HostReach.live),
    ];

    expect(
      placeHost(hosts, null),
      'local',
      reason: 'the first host that is answering now, not the first listed',
    );
  });

  test('with no live host the first host is read', () {
    final hosts = [
      host('studio', HostReach.unreachable),
      host('local', HostReach.stale),
    ];

    expect(placeHost(hosts, null), 'studio');
  });

  test('an unknown scope reads as all hosts', () {
    final hosts = [
      host('studio', HostReach.unreachable),
      host('local', HostReach.live),
    ];

    // A name discovery no longer finds is not an error: the place opens, on
    // the host all hosts would have read.
    expect(placeHost(hosts, 'retired'), 'local');
  });

  test('no hosts, no host', () {
    expect(placeHost(const [], null), isNull);
    expect(placeHost(const [], 'local'), isNull);
  });
}
