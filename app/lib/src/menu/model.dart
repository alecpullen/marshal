import '../daemon/status.dart';

/// TrayItem is one clickable row. id is what the app switches on.
class TrayItem {
  const TrayItem({
    required this.id,
    required this.label,
    this.enabled = true,
    this.checkbox = false,
    this.checked = false,
  });

  final String id;
  final String label;
  final bool enabled;

  /// Whether the row is drawn as a checkbox. It is separate from [checked]
  /// because an unchecked checkbox is still a checkbox: launch-at-login must
  /// read as a toggle that is off, not as a plain action.
  final bool checkbox;
  final bool checked;

  @override
  String toString() => 'TrayItem($id, "$label", enabled: $enabled)';
}

/// TraySeparator draws a dividing line.
class TraySeparator {
  const TraySeparator();

  @override
  String toString() => 'TraySeparator()';
}

/// TrayMenu is the menu as data, so it can be tested without a tray.
class TrayMenu {
  const TrayMenu({required this.title, required this.entries});

  final String title;
  final List<Object> entries;

  /// item returns the entry with this id, or null when the menu does not
  /// offer it. Exactly one of install/start/stop is ever present.
  TrayItem? item(String id) {
    for (final entry in entries) {
      if (entry is TrayItem && entry.id == id) return entry;
    }
    return null;
  }
}

/// menuFor turns one status into the menu. A null status with a failure is
/// the app's own failure to read it; a null status without one means "not
/// read yet".
///
/// A [failure] beside a status keeps the last good status — the menu still
/// names the daemon and still offers its actions — and says the state is
/// stale, rather than emptying itself over one failed poll.
TrayMenu menuFor({
  required String appVersion,
  required bool logExists,
  required bool launchAtLogin,
  DaemonStatus? status,
  String? failure,
}) {
  final entries = <Object>[
    TrayItem(
      id: 'status',
      label: _statusLine(appVersion, status, failure),
      enabled: false,
    ),
    const TraySeparator(),
    const TrayItem(id: 'fleet', label: 'Open Fleet…'),
  ];

  if (status != null) {
    if (!status.service.installed) {
      entries.add(
        const TrayItem(id: 'install', label: 'Install Marshal Daemon…'),
      );
    } else if (status.service.running) {
      entries.add(const TrayItem(id: 'stop', label: 'Stop Daemon'));
    } else {
      entries.add(const TrayItem(id: 'start', label: 'Start Daemon'));
    }
  }

  entries
    ..add(const TraySeparator())
    ..add(TrayItem(id: 'log', label: 'Open Log…', enabled: logExists))
    ..add(
      TrayItem(
        id: 'login',
        label: 'Launch at Login',
        checkbox: true,
        checked: launchAtLogin,
      ),
    )
    ..add(const TraySeparator())
    ..add(const TrayItem(id: 'quit', label: 'Quit'));

  return TrayMenu(title: 'Marshal', entries: entries);
}

String _statusLine(String appVersion, DaemonStatus? status, String? failure) {
  if (status == null) {
    return failure != null
        ? 'Marshal — status unavailable'
        : 'Marshal — starting…';
  }
  final line = _stateLine(appVersion, status);
  return failure != null ? '$line · status unavailable' : line;
}

String _stateLine(String appVersion, DaemonStatus status) {
  if (!status.service.installed) return 'Marshal — not installed';
  final daemon = status.daemon;
  if (daemon == null) return 'Marshal — daemon stopped';
  final projects = daemon.projects.length;
  final noun = projects == 1 ? 'project' : 'projects';
  final line =
      'Marshal — running (pid ${status.service.pid}) · $projects $noun';
  // Version skew is visible rather than silent: a bundled app and an
  // installed daemon can be different builds.
  if (daemon.version.isNotEmpty && daemon.version != appVersion) {
    return '$line · daemon ${daemon.version} (app $appVersion)';
  }
  return line;
}
