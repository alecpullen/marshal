@TestOn('!windows')
library;

import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/fleet_profiles.dart'
    show FleetProfileFailure;
import 'package:marshal_app/src/client/fleet_profiles_io.dart';

/// The native runner against a fake `marshal`: a shell script in a temporary
/// directory that records the arguments it was given and exits with a code the
/// test chooses.
///
/// What the app must not get wrong is the command it runs (D6): the recording
/// is the assertion rather than a mock of the process.
void main() {
  late Directory dir;

  setUp(() async {
    dir = await Directory.systemTemp.createTemp('marshal-fleet-profiles-');
    addTearDown(() => dir.delete(recursive: true));
  });

  /// Writes the fake binary into [dir] and answers its path. It records its
  /// arguments in `argv` and its stdin in `stdin`, writes [stderr] to its
  /// stderr, and exits with [exitCode].
  Future<String> fakeMarshal({String stderr = '', int exitCode = 0}) async {
    final argv = File('${dir.path}/argv');
    final stdin = File('${dir.path}/stdin');
    final err = File('${dir.path}/stderr');
    await err.writeAsString(stderr);
    final script = File('${dir.path}/marshal');
    await script.writeAsString('''
#!/bin/sh
printf '%s\\n' "\$@" > '${argv.path}'
cat > '${stdin.path}'
cat '${err.path}' >&2
exit $exitCode
''');
    final chmod = await Process.run('chmod', ['755', script.path]);
    expect(chmod.exitCode, 0, reason: 'the fake marshal must be executable');
    return script.path;
  }

  Future<List<String>> recorded(String name) =>
      File('${dir.path}/$name').readAsLines();

  test('add passes the name and the destination', () async {
    final binary = await fakeMarshal();
    final profiles = fleetProfiles(binary: binary);

    await profiles.add('box', 'me@box');
    expect(await recorded('argv'), ['fleet', 'add', 'box', 'me@box']);

    // A destination that is a Host from ~/.ssh/config is passed as it is: ssh
    // is what reads it.
    await profiles.add('lan', 'box');
    expect(await recorded('argv'), ['fleet', 'add', 'lan', 'box']);
  });

  test('a failure carries the last line of stderr', () async {
    final binary = await fakeMarshal(
      stderr:
          'marshal fleet add: reading fleet.toml\n'
          'marshal fleet add: the name "home" is taken\n',
      exitCode: 2,
    );

    await expectLater(
      fleetProfiles(binary: binary).add('home', 'me@home'),
      throwsA(
        isA<FleetProfileFailure>().having(
          (failure) => failure.message,
          'message',
          'marshal fleet add: the name "home" is taken',
        ),
      ),
    );
  });

  test('an empty stderr names the exit code', () async {
    final binary = await fakeMarshal(exitCode: 3);

    await expectLater(
      fleetProfiles(binary: binary).remove('home'),
      throwsA(
        isA<FleetProfileFailure>().having(
          (failure) => failure.message,
          'message',
          'marshal exited 3',
        ),
      ),
    );
  });
}
