@TestOn('!windows')
library;

import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/host.dart';
import 'package:marshal_app/src/client/host_discovery_io.dart';
import 'package:marshal_app/src/daemon/cli.dart';

import '../support/fake_bridge.dart';

/// Native discovery against a fake `marshal`: a shell script in a temporary
/// directory that answers `daemon status --json` with the document a test
/// names, fails anything else, and records every call it was given.
///
/// The hosts a daemon reaches are its own tunnels, so the document is what
/// discovery reads and the tunnels' sockets are what it dials (one-daemon D9).
void main() {
  late Directory dir;

  setUp(() async {
    dir = await Directory.systemTemp.createTemp('marshal-host-discovery-');
    addTearDown(() => dir.delete(recursive: true));
    // The test binding answers every HttpClient request 400 so no test
    // reaches the network. This file's hosts are sockets the test starts and
    // owns, so the real client is the fixture being exercised.
    HttpOverrides.global = null;
  });

  /// Writes the fake binary into [dir] and answers its path. It answers
  /// `daemon status --json` with [document], anything else with a failure, and
  /// records each call it ran, one per line, in `calls`.
  Future<String> fakeMarshal(Map<String, Object?> document) async {
    final status = File('${dir.path}/status.json');
    await status.writeAsString(jsonEncode(document));
    final script = File('${dir.path}/marshal');
    await script.writeAsString('''
#!/bin/sh
printf '%s\\n' "\$*" >> '${dir.path}/calls'
case "\$1 \$2" in
  "daemon status") cat '${status.path}' ;;
  *) exit 2 ;;
esac
''');
    final chmod = await Process.run('chmod', ['755', script.path]);
    expect(chmod.exitCode, 0, reason: 'the fake marshal must be executable');
    return script.path;
  }

  /// The status document a test names: a daemon answering at [socket] with
  /// [hosts] as its tunnels.
  Map<String, Object?> statusDocument(
    String socket, {
    List<Map<String, Object?>> hosts = const [],
  }) => {
    'schemaVersion': 1,
    'answering': true,
    'service': {'installed': true, 'running': true, 'pid': 1234},
    'linger': {'supported': true, 'enabled': true},
    'daemon': {
      'version': '0.9.1',
      'socket': socket,
      'projects': <String>[],
      'children': <Object?>[],
      'hosts': hosts,
    },
  };

  /// Every call the script ran, in the order it ran them.
  Future<List<String>> calls() => File('${dir.path}/calls').readAsLines();

  test('the local daemon comes first, then each host', () async {
    final binary = await fakeMarshal(
      statusDocument(
        '${dir.path}/daemon.sock',
        hosts: [
          {
            'name': 'box',
            'ssh': 'me@box',
            'state': 'live',
            'socket': '${dir.path}/box.sock',
            'since': 1763300000000,
          },
          {
            'name': 'tower',
            'ssh': 'me@tower',
            'state': 'down',
            'socket': '${dir.path}/tower.sock',
            'lastError': 'connection refused',
            'since': 1763300001000,
          },
        ],
      ),
    );

    final hosts = await discoverHosts(cli: DaemonCli(binary: binary));

    expect(
      [for (final host in hosts) host.name],
      [localHostName, 'box', 'tower'],
      reason: 'the daemon it is reached through comes first',
    );
    expect(
      [for (final host in hosts) host.kind],
      [HostKind.local, HostKind.server, HostKind.server],
    );
  });

  test('a host carries its destination and dials its socket', () async {
    // The socket the document names is the host's own end of its tunnel, so a
    // bridge there is what the host's client reaches.
    final socket = '${dir.path}/box.sock';
    final box = await FakeBridge.onUnixSocket(socket);
    addTearDown(box.close);
    box.reply('GET', '/api/agents', const FakeReply(200, body: '[]'));

    final binary = await fakeMarshal(
      statusDocument(
        '${dir.path}/daemon.sock',
        hosts: [
          {
            'name': 'box',
            'ssh': 'me@box',
            'state': 'live',
            'socket': socket,
            'since': 1763300000000,
          },
        ],
      ),
    );

    final hosts = await discoverHosts(cli: DaemonCli(binary: binary));
    final host = hosts.singleWhere((host) => host.name == 'box');

    expect(host.destination, 'me@box');
    expect(host.kind, HostKind.server);
    expect(await host.client.agents(), isEmpty);
    expect(box.requests.single.uri, '/api/agents');
  });

  test('a host with no socket is not listed', () async {
    // A tunnel that has not bound its socket yet reaches nothing, so the app
    // lists no host it could not dial.
    final binary = await fakeMarshal(
      statusDocument(
        '${dir.path}/daemon.sock',
        hosts: [
          {
            'name': 'box',
            'ssh': 'me@box',
            'state': 'connecting',
            'socket': '',
            'since': 0,
          },
        ],
      ),
    );

    final hosts = await discoverHosts(cli: DaemonCli(binary: binary));

    expect([for (final host in hosts) host.name], [localHostName]);
  });

  test('a silent daemon lists no hosts', () async {
    // The hosts are that daemon's tunnels: with no daemon answering there is
    // no tunnel to reach either.
    final binary = await fakeMarshal({
      'schemaVersion': 1,
      'answering': false,
      'service': {'installed': true, 'running': false, 'pid': 0},
      'linger': {'supported': true, 'enabled': false},
    });

    final hosts = await discoverHosts(cli: DaemonCli(binary: binary));

    expect(hosts, isEmpty);
  });

  test('discovery runs only the status command', () async {
    // The app runs no `fleet` command and reads no profile: every host is one
    // the daemon's document names.
    final binary = await fakeMarshal(
      statusDocument(
        '${dir.path}/daemon.sock',
        hosts: [
          {
            'name': 'box',
            'ssh': 'me@box',
            'state': 'live',
            'socket': '${dir.path}/box.sock',
            'since': 1763300000000,
          },
        ],
      ),
    );

    await discoverHosts(cli: DaemonCli(binary: binary));

    expect(await calls(), ['daemon status --json']);
  });
}
