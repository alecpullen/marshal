@TestOn('!windows')
library;

import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/host_discovery_io.dart';
import 'package:marshal_app/src/daemon/cli.dart';

/// Native discovery against a fake `marshal`: a shell script in a temporary
/// directory that answers `fleet servers --json` with the document a test
/// names and `fleet token NAME` with a token of its own, and records every
/// call it was given.
///
/// A relayed profile is the case worth holding: its transport is its hub's,
/// built from the hub's row, and the token it carries is its own — so what the
/// script was asked for and what arrived at the hub are the assertion, rather
/// than a mock of either.
void main() {
  late Directory dir;

  setUp(() async {
    dir = await Directory.systemTemp.createTemp('marshal-host-discovery-');
    addTearDown(() => dir.delete(recursive: true));
    // The test binding answers every HttpClient request 400 so no test
    // reaches the network. This file's hub is a loopback server the test
    // starts and owns, so the real client is the fixture being exercised.
    HttpOverrides.global = null;
  });

  /// Writes the fake binary into [dir] and answers its path. It answers
  /// `fleet servers --json` with [servers], `fleet token NAME` with
  /// `token-NAME`, anything else with a failure, and records each call it ran,
  /// one per line, in `calls`.
  Future<String> fakeMarshal(List<Map<String, Object?>> servers) async {
    final document = File('${dir.path}/servers.json');
    await document.writeAsString(
      jsonEncode({'schemaVersion': 1, 'servers': servers}),
    );
    final script = File('${dir.path}/marshal');
    await script.writeAsString('''
#!/bin/sh
printf '%s\\n' "\$*" >> '${dir.path}/calls'
case "\$1 \$2" in
  "fleet servers") cat '${document.path}' ;;
  "fleet token") printf 'token-%s\\n' "\$3" ;;
  *) exit 2 ;;
esac
''');
    final chmod = await Process.run('chmod', ['755', script.path]);
    expect(chmod.exitCode, 0, reason: 'the fake marshal must be executable');
    return script.path;
  }

  /// Every call the script ran, in the order it ran them.
  Future<List<String>> calls() => File('${dir.path}/calls').readAsLines();

  test('a relayed profile reaches its host through its hub', () async {
    // A hub that answers as a bridge does: what the relayed host sends must
    // arrive here, under that host's name, carrying both tokens.
    final hub = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    addTearDown(() => hub.close(force: true));
    final seen = <({String path, String? hostToken, String? authorization})>[];
    hub.listen((request) async {
      seen.add((
        path: request.uri.path,
        hostToken: request.headers.value('x-marshal-host-token'),
        authorization: request.headers.value(HttpHeaders.authorizationHeader),
      ));
      request.response
        ..statusCode = 200
        ..write('[]');
      await request.response.close();
    });

    final binary = await fakeMarshal([
      {'name': 'hub', 'url': 'http://127.0.0.1:${hub.port}'},
      {'name': 'laptop', 'relay': 'hub'},
    ]);
    final hosts = await discoverHosts(cli: DaemonCli(binary: binary));
    final laptop = hosts.singleWhere((host) => host.name == 'laptop');
    expect(laptop.via, 'hub', reason: 'the host says where it is reached');

    expect(await laptop.client.agents(), isEmpty);
    expect(seen, hasLength(1));
    expect(seen.single.path, '/api/hosts/laptop/api/agents');
    expect(seen.single.hostToken, 'token-laptop');
    expect(seen.single.authorization, 'Bearer token-hub');

    // Both tokens came from the script, the host's own first: the relay bears
    // the host's token to it, and its own to the hub.
    final ran = await calls();
    expect(ran, containsAllInOrder(['fleet token laptop', 'fleet token hub']));
  });

  test('a relayed profile whose hub is missing is skipped', () async {
    final binary = await fakeMarshal([
      {'name': 'laptop', 'relay': 'hub'},
    ]);

    final hosts = await discoverHosts(cli: DaemonCli(binary: binary));

    expect(
      hosts,
      isEmpty,
      reason: 'a hub the document does not name reaches nothing',
    );
  });
}
