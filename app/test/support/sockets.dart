import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/transport.dart';

import 'fake_bridge.dart';

/// A directory short enough to hold a unix socket, removed when the test ends.
///
/// A unix socket path is capped at 103 bytes on macOS, and the platform's own
/// temporary directory (`/var/folders/…` there) can leave too little room once
/// a test's prefix and a socket's name are added. `/tmp` is short wherever
/// these tests run, so the fixtures use it.
Directory socketDir() {
  final tmp = Directory('/tmp');
  final dir = tmp.existsSync()
      ? tmp.createTempSync('marshal-')
      : Directory.systemTemp.createTempSync('marshal-');
  addTearDown(() {
    if (dir.existsSync()) dir.deleteSync(recursive: true);
  });
  return dir;
}

/// A fake bridge on a unix socket, and the transport the app reaches one with:
/// the way it reaches a daemon and each host's own end of its tunnel. Every
/// test that talks to a bridge uses this, since a loopback origin is no longer
/// a transport the app has.
class SocketBridge {
  SocketBridge._(this.bridge, this.transport);

  /// The bridge itself: its canned routes, its streams and what it was sent.
  final FakeBridge bridge;

  /// The transport that reaches it, as the app's own transport does.
  final Transport transport;

  /// The socket the bridge listens on.
  String get socketPath => bridge.socketPath!;
}

/// Starts a [SocketBridge] on [name], in a directory short enough for the
/// socket's path, and closes it when the test ends.
Future<SocketBridge> socketBridge([String name = 'bridge.sock']) async {
  final path = '${socketDir().path}/$name';
  final bridge = await FakeBridge.onUnixSocket(path);
  addTearDown(bridge.close);
  return SocketBridge._(bridge, unixTransport(path));
}

/// A transport whose socket nothing listens on: a bridge that is not there, for
/// the tests that need an answer to never arrive.
Transport unreachableTransport() =>
    unixTransport('${socketDir().path}/nothing.sock');
