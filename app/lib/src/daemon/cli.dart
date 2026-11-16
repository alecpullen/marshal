import 'dart:convert';
import 'dart:io';

import 'status.dart';

/// DaemonCli runs the marshal binary the app ships. Everything the app knows
/// about Marshal comes through it: the app never opens the daemon's socket
/// and never reads its files.
///
/// This is the only file under `lib/` allowed to import `dart:io`; every
/// other file is pure Dart, so the menu and the models are tested without a
/// process, a socket or a display.
class DaemonCli {
  DaemonCli({String? binary}) : _binary = binary;

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

  /// status reads one status document. It throws [DaemonException] when the
  /// binary said nothing or when its output is not a document this app
  /// speaks.
  Future<DaemonStatus> status() async {
    final result = await Process.run(binary, const [
      'daemon',
      'status',
      '--json',
    ]);
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
  /// failed, carrying the binary's own message.
  Future<void> run(List<String> args) async {
    final result = await Process.run(binary, args);
    if (result.exitCode != 0) {
      final err = (result.stderr as String).trim();
      throw DaemonException(
        err.isEmpty
            ? 'marshal ${args.join(' ')} exited ${result.exitCode}'
            : err,
      );
    }
  }

  /// open hands a path to the desktop: `open` on macOS, `xdg-open` on Linux.
  Future<void> open(String target) async {
    final opener = Platform.isMacOS ? 'open' : 'xdg-open';
    await Process.run(opener, [target]);
  }
}

/// DaemonException is a marshal invocation the app could not use, with the
/// message the menu shows.
class DaemonException implements Exception {
  DaemonException(this.message);

  final String message;

  @override
  String toString() => message;
}
