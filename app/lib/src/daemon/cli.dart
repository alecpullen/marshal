import 'dart:convert';
import 'dart:io';

import 'status.dart';

/// _enoent is errno 2, no such file or directory: what `Process.run`
/// completes with when the executable it was given is not there, on every
/// platform this app runs on.
const _enoent = 2;

/// DaemonCli runs the marshal binary the app ships. Everything the tray
/// knows about Marshal comes through it: the tray never opens the daemon's
/// socket and never reads its files.
///
/// Outside `lib/src/client/`'s `*_io.dart` transports, this is the only file
/// under `lib/` allowed to import `dart:io` (`test/boundary_test.dart`); the
/// tray's other files are pure Dart, so the menu and the models are tested
/// without a process, a socket or a display.
class DaemonCli {
  DaemonCli({this._binary});

  final String? _binary;

  String get binary => _binary ?? _locate();

  /// _locate prefers the binary inside the app bundle, then a developer's
  /// own install, and finally PATH.
  String _locate() {
    final exe = File(Platform.resolvedExecutable).parent;
    final candidates = <String>[
      if (Platform.isMacOS) '${exe.parent.path}/Resources/bin/marshal',
      if (Platform.isLinux) '${exe.path}/bin/marshal',
      '/usr/local/bin/marshal',
      '${Platform.environment['HOME'] ?? ''}/.local/bin/marshal',
    ];
    for (final path in candidates) {
      if (File(path).existsSync()) return path;
    }
    return 'marshal';
  }

  /// status reads one status document. It throws [MarshalMissingException]
  /// when there is no marshal binary to run, and [DaemonException] when the
  /// binary said nothing or when its output is not a document this app
  /// speaks.
  Future<DaemonStatus> status() async {
    final result = await _spawn(const ['daemon', 'status', '--json']);
    final out = (result.stdout as String).trim();
    if (out.isEmpty) {
      final err = (result.stderr as String).trim();
      throw DaemonException(
        err.isEmpty ? 'marshal daemon status said nothing' : err,
      );
    }
    return DaemonStatus.fromJson(jsonDecode(out) as Map<String, dynamic>);
  }

  /// run runs one marshal subcommand and throws [DaemonException] if it
  /// failed, carrying the binary's own message; a missing binary is
  /// [MarshalMissingException] here too, so what a broken bundle reports
  /// does not depend on which action hit it.
  Future<void> run(List<String> args) async {
    final result = await _spawn(args);
    if (result.exitCode != 0) {
      final err = (result.stderr as String).trim();
      throw DaemonException(
        err.isEmpty
            ? 'marshal ${args.join(' ')} exited ${result.exitCode}'
            : err,
      );
    }
  }

  /// _spawn runs the binary with `args` and names a missing binary
  /// specifically: an absent `marshal` is [MarshalMissingException], §5's
  /// own menu state, while any other spawn failure keeps the raw error — a
  /// permission or an OS problem is not "marshal not found".
  Future<ProcessResult> _spawn(List<String> args) async {
    try {
      return await Process.run(binary, args);
    } on ProcessException catch (error) {
      if (error.errorCode == _enoent) throw const MarshalMissingException();
      rethrow;
    }
  }

  /// open hands a path to the desktop: `open` on macOS, `xdg-open` on Linux.
  Future<void> open(String target) async {
    final opener = Platform.isMacOS ? 'open' : 'xdg-open';
    await Process.run(opener, [target]);
  }

  /// executable is the running binary, which `launch_at_startup` needs to
  /// name the login item. Here because this file owns `dart:io`.
  String get executable => Platform.resolvedExecutable;

  /// logPath is the daemon's log — `daemon.Paths.Log` in Go: the data
  /// directory's `daemon/webbridge.log`. `$MARSHAL_DATA_DIR` overrides the
  /// data directory, then `$XDG_DATA_HOME/marshal`, then
  /// `~/.local/share/marshal`, in the same order the daemon resolves it.
  String get logPath {
    final env = Platform.environment;
    final data =
        env['MARSHAL_DATA_DIR'] ??
        (env['XDG_DATA_HOME'] != null
            ? '${env['XDG_DATA_HOME']}/marshal'
            : '${env['HOME'] ?? ''}/.local/share/marshal');
    return '$data/daemon/webbridge.log';
  }

  /// logExists is whether there is a log to open. The menu disables
  /// `Open Log…` when there is not, so it never offers a dead action.
  bool get logExists => File(logPath).existsSync();

  /// log appends one line to the daemon's log. It is where a message with no
  /// window to go in ends up: the pinned tray plugin has no notification
  /// API, so the menu's error reports land in the log the menu can open.
  void log(String message) {
    final file = File(logPath);
    try {
      file.parent.createSync(recursive: true);
      file.writeAsStringSync(
        '${DateTime.now().toUtc().toIso8601String()} marshal-app: $message\n',
        mode: FileMode.append,
        flush: true,
      );
    } on FileSystemException {
      // A log write must never take the tray down with it.
    }
  }

  /// quit ends the app. It lives here with the rest of the process I/O so no
  /// other file under `lib/` needs `dart:io`.
  void quit() => exit(0);
}

/// DaemonException is a marshal invocation the app could not use, with the
/// message the menu shows.
class DaemonException implements Exception {
  DaemonException(this.message);

  final String message;

  @override
  String toString() => message;
}

/// MarshalMissingException is no marshal binary at all: the app's bundle
/// lost its binaries, or PATH has no marshal. It is distinct from
/// [DaemonException] because the menu treats it differently — §5 makes a
/// missing binary non-fatal, read as `marshal not found` with the daemon
/// actions dropped, where a binary that answered wrongly is the ordinary
/// stale-status state.
class MarshalMissingException implements Exception {
  const MarshalMissingException();

  @override
  String toString() => 'marshal not found';
}
