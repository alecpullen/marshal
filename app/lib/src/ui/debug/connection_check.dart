import '../kit/kit.dart';

/// What the connection check can say about one host. It mirrors the client's
/// `HostReach` without importing `client/`, which `ui/` never does
/// (design-system.md §5: screens take data, they never fetch).
enum HostReachView { connecting, live, stale, unreachable, unauthorized }

/// One host as the connection check shows it.
@immutable
class HostRow {
  const HostRow({
    required this.name,
    required this.kind,
    required this.reach,
    this.lastContact,
    this.agentCount,
  });

  final String name;

  /// "local" or "server": a host's kind in words (§8: roles are named in
  /// words, not told apart by colour).
  final String kind;

  final HostReachView reach;

  /// When the host last said anything, or null when it never has.
  final DateTime? lastContact;

  /// How many agents the host runs, or null when its count could not be read.
  final int? agentCount;
}

/// The debug connection check (F1): each host the app can reach, its state,
/// when it was last heard from, and how many agents it runs.
///
/// It is reachable from the gallery entry only: `flutter run -t
/// lib/gallery_main.dart --dart-define=connection-check=true`. It is
/// registered with the enforcement tests in `test/support/screens.dart`.
class ConnectionCheck extends StatelessWidget {
  const ConnectionCheck({
    super.key,
    this.hosts = const [],
    this.state,
    this.now,
    this.onRetry,
  });

  final List<HostRow> hosts;

  /// The §7 state the screen is in, or null for its data.
  final StateKind? state;

  /// The clock relative ages are measured against; a test supplies one.
  final DateTime? now;

  /// What "Try again" does, when there is anything to try again.
  final VoidCallback? onRetry;

  @override
  Widget build(BuildContext context) {
    return PageBody(
      title: 'Connection check',
      subtitle: 'Each host this app can reach, its state and its agents',
      width: PageWidth.list,
      children: _content(now ?? DateTime.now()),
    );
  }

  List<Widget> _content(DateTime at) {
    switch (state) {
      case StateKind.loading:
        return const [StateMessage.loading()];
      case StateKind.planned:
        return const [
          StateMessage.planned(
            name: 'Nothing here yet',
            note: 'A host appears once one answers.',
          ),
        ];
      case StateKind.empty:
        return [
          StateMessage.empty(
            message: 'No host has answered yet',
            action: _retry('Try again'),
          ),
        ];
      case StateKind.error:
        return [
          StateMessage.error(
            message: 'The hosts could not be reached',
            retry: _retry('Try again'),
          ),
        ];
      case StateKind.unauthorized:
        return const [StateMessage.unauthorized()];
      case StateKind.unavailable:
        final first = hosts.isEmpty ? null : hosts.first;
        return [
          StateMessage.unavailable(
            host: first?.name ?? 'Every host',
            lastSeen: _seen(first, at),
          ),
        ];
      case StateKind.stale:
        return [
          StateMessage.stale(age: _age(_newest(), at)),
          const SizedBox(height: Space.s12),
          ..._hosts(at),
        ];
      case null:
        return _hosts(at);
    }
  }

  List<Widget> _hosts(DateTime at) {
    if (hosts.isEmpty) {
      return [
        StateMessage.empty(
          message: 'No host has answered yet',
          action: _retry('Try again'),
        ),
      ];
    }
    return [
      Label('Hosts', count: hosts.length),
      const SizedBox(height: Space.s8),
      Rows([
        for (final host in hosts)
          RowItem(
            name: host.name,
            description:
                '${host.kind} · ${_reach(host.reach)} · ${_seen(host, at)}',
            value: _agents(host.agentCount),
          ),
      ]),
    ];
  }

  StateAction _retry(String label) =>
      StateAction(label, onTap: onRetry ?? () {});

  /// The newest contact among the hosts, for the stale screen's age.
  DateTime? _newest() {
    DateTime? newest;
    for (final host in hosts) {
      final contact = host.lastContact;
      if (contact == null) continue;
      if (newest == null || contact.isAfter(newest)) newest = contact;
    }
    return newest;
  }
}

/// A host's reach in words, never as a colour alone (§2).
String _reach(HostReachView reach) => switch (reach) {
  HostReachView.connecting => 'connecting',
  HostReachView.live => 'live',
  HostReachView.stale => 'stale',
  HostReachView.unreachable => 'unreachable',
  HostReachView.unauthorized => 'token refused',
};

/// When a host was last heard from, relatively and shortly (§8).
String _seen(HostRow? host, DateTime at) {
  final contact = host?.lastContact;
  if (contact == null) return 'not reached yet';
  return 'last seen ${_age(contact, at)}';
}

/// How many agents a host runs, or that its count is unavailable.
String _agents(int? count) {
  if (count == null) return 'agents unavailable';
  if (count == 1) return '1 agent';
  return '$count agents';
}

/// How long ago [then] was, in §8's short form: "8m", "1h 12m", "2d".
String _age(DateTime? then, DateTime at) {
  if (then == null) return 'not reached yet';
  final age = at.difference(then);
  if (age.inSeconds < 60) return 'now';
  if (age.inMinutes < 60) return '${age.inMinutes}m';
  if (age.inHours < 24) {
    final minutes = age.inMinutes % 60;
    return minutes == 0 ? '${age.inHours}h' : '${age.inHours}h ${minutes}m';
  }
  return '${age.inDays}d';
}
