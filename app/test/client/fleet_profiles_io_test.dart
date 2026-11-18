@TestOn('!windows')
library;

import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/fleet_profiles.dart'
    show FleetProfileFailure;
import 'package:marshal_app/src/client/fleet_profiles_io.dart';

/// The native runner against a fake `marshal`: a shell script in a temporary
/// directory that records the arguments and the stdin it was given and exits
/// with a code the test chooses.
///
/// What the app must never get wrong is D7's one rule — the token reaches
/// `fleet login` on its stdin, in no argument and in no environment variable —
/// so the recording is the assertion rather than a mock of the process.
void main() {
  late Directory dir;

  setUp(() async {
    dir = await Directory.systemTemp.createTemp('marshal-fleet-profiles-');
    addTearDown(() => dir.delete(recursive: true));
  });

  /// Writes the fake binary into [dir] and answers its path. It records its
  /// arguments in `argv`, its stdin in `stdin`, writes [stderr] to its stderr,
  /// and exits with [exitCode].
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

  test('add passes the name, the url and the ca file', () async {
    final binary = await fakeMarshal();
    final profiles = fleetProfiles(binary: binary);

    await profiles.add(
      'home',
      Uri.parse('https://marshal.example.com'),
      caFile: '/certs/ca.pem',
    );
    expect(await recorded('argv'), [
      'fleet',
      'add',
      'home',
      'https://marshal.example.com',
      '--ca-file',
      '/certs/ca.pem',
    ]);

    // A profile with no CA file states none: an empty flag would make the
    // CLI refuse the file it was told to trust.
    await profiles.add('lan', Uri.parse('https://192.168.1.2:7700'));
    expect(await recorded('argv'), [
      'fleet',
      'add',
      'lan',
      'https://192.168.1.2:7700',
    ]);
  });

  test('login writes the token to stdin and never to argv', () async {
    final binary = await fakeMarshal();

    await fleetProfiles(binary: binary).login('home', 's3cr3t-token');

    final argv = await recorded('argv');
    expect(argv, ['fleet', 'login', 'home']);
    expect(argv.join(' '), isNot(contains('s3cr3t-token')));
    expect(await File('${dir.path}/stdin').readAsString(), 's3cr3t-token\n');
  });

  test('a failure carries the last line of stderr', () async {
    final binary = await fakeMarshal(
      stderr:
          'marshal fleet add: reading fleet.toml\n'
          'marshal fleet add: the name "home" is taken\n',
      exitCode: 2,
    );

    await expectLater(
      fleetProfiles(binary: binary)
          .add('home', Uri.parse('https://marshal.example.com')),
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
