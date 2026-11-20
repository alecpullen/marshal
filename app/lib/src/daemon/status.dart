/// The models in this file are pure Dart: no I/O library, no Flutter, no
/// plugin. They mirror `marshal daemon status --json`
/// (`internal/daemon/status.go`), and the test beside them parses the fixture
/// the Go test holds that document to.
library;

/// DaemonStatus is one `marshal daemon status --json` document.
class DaemonStatus {
  const DaemonStatus({
    required this.schemaVersion,
    required this.answering,
    required this.service,
    required this.linger,
    this.daemon,
  });

  /// The schema this app speaks. A document at another version is refused
  /// rather than guessed at.
  static const supportedSchema = 1;

  final int schemaVersion;

  /// Whether the daemon's socket answered. `daemon` is null when it did not,
  /// so this is the same fact explicitly, as the Go document carries it.
  final bool answering;

  final ServiceState service;
  final LingerState linger;
  final DaemonInfo? daemon;

  factory DaemonStatus.fromJson(Map<String, dynamic> json) {
    final version = json['schemaVersion'] as int? ?? 0;
    if (version != supportedSchema) {
      throw FormatException(
        'daemon status schema $version, this app speaks $supportedSchema',
      );
    }
    final daemon = json['daemon'];
    return DaemonStatus(
      schemaVersion: version,
      answering: json['answering'] as bool? ?? false,
      service: ServiceState.fromJson(
        (json['service'] as Map<String, dynamic>?) ?? const {},
      ),
      linger: LingerState.fromJson(
        (json['linger'] as Map<String, dynamic>?) ?? const {},
      ),
      daemon: daemon == null
          ? null
          : DaemonInfo.fromJson(daemon as Map<String, dynamic>),
    );
  }
}

/// ServiceState is what the OS service manager reports for the daemon's unit.
class ServiceState {
  const ServiceState({
    required this.installed,
    required this.running,
    required this.pid,
  });

  final bool installed;
  final bool running;
  final int pid;

  factory ServiceState.fromJson(Map<String, dynamic> json) => ServiceState(
    installed: json['installed'] as bool? ?? false,
    running: json['running'] as bool? ?? false,
    pid: json['pid'] as int? ?? 0,
  );
}

/// LingerState is whether the daemon survives logout. macOS has no such bit,
/// so [`supported`] is false there and the document still carries the object.
class LingerState {
  const LingerState({required this.supported, required this.enabled});

  final bool supported;
  final bool enabled;

  factory LingerState.fromJson(Map<String, dynamic> json) => LingerState(
    supported: json['supported'] as bool? ?? false,
    enabled: json['enabled'] as bool? ?? false,
  );
}

/// DaemonInfo is the answering daemon's own report, the part of the document
/// the menu names.
class DaemonInfo {
  const DaemonInfo({
    required this.version,
    required this.projects,
    required this.children,
    this.socket = '',
    this.hosts = const [],
  });

  final String version;
  final List<String> projects;
  final List<DaemonChild> children;

  /// The daemon's socket, which a client dials to reach it (F1 decision 4:
  /// the app reads this rather than computing the path).
  final String socket;

  /// The hosts the daemon keeps a tunnel to, which is every host this app can
  /// reach beyond the daemon itself (one-daemon D9).
  final List<DaemonHost> hosts;

  factory DaemonInfo.fromJson(Map<String, dynamic> json) => DaemonInfo(
    version: json['version'] as String? ?? '',
    // toList forces the cast now: a lazy cast throws only when read,
    // which for projects is menu-build time, outside the poll's try —
    // an uncaught async error that would take the tray down.
    projects: ((json['projects'] as List<dynamic>?) ?? const [])
        .cast<String>()
        .toList(),
    children: ((json['children'] as List<dynamic>?) ?? const [])
        .map((e) => DaemonChild.fromJson(e as Map<String, dynamic>))
        .toList(),
    socket: json['socket'] as String? ?? '',
    hosts: ((json['hosts'] as List<dynamic>?) ?? const [])
        .map((e) => DaemonHost.fromJson(e as Map<String, dynamic>))
        .toList(),
  );
}

/// DaemonChild is one project child of the daemon.
class DaemonChild {
  const DaemonChild({
    required this.root,
    required this.version,
    required this.pinned,
  });

  final String root;
  final String version;
  final bool pinned;

  factory DaemonChild.fromJson(Map<String, dynamic> json) => DaemonChild(
    root: json['root'] as String? ?? '',
    version: json['version'] as String? ?? '',
    pinned: json['pinned'] as bool? ?? false,
  );
}

/// DaemonHost is one host the daemon keeps a tunnel to, as its status document
/// lists it (one-daemon D7, D9).
///
/// [`socket`] is this machine's end of that tunnel, which is what a client
/// dials to reach the host; [`state`] is the tunnel's state in the daemon's own
/// words, and [`lastError`] is why it last failed, when it did.
class DaemonHost {
  const DaemonHost({
    required this.name,
    required this.ssh,
    required this.state,
    required this.socket,
    this.lastError = '',
    this.since = 0,
  });

  final String name;
  final String ssh;
  final String state;
  final String socket;
  final String lastError;

  /// Unix milliseconds: when [`state`] last changed.
  final int since;

  factory DaemonHost.fromJson(Map<String, dynamic> json) => DaemonHost(
    name: json['name'] as String? ?? '',
    ssh: json['ssh'] as String? ?? '',
    state: json['state'] as String? ?? '',
    socket: json['socket'] as String? ?? '',
    lastError: json['lastError'] as String? ?? '',
    since: json['since'] as int? ?? 0,
  );
}
