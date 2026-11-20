@TestOn('browser')
library;

import 'dart:js_interop';
import 'dart:js_interop_unsafe';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/host.dart';
import 'package:marshal_app/src/client/host_discovery_stub.dart';
import 'package:web/web.dart' as web;

/// The browser's discovery, run in Chrome: the daemon that served the page is
/// the first host, and every tunnel it reports is a host reached through it
/// (one-daemon D10).
///
/// `GET /api/hosts` is answered by a fake `fetch`, so a test never needs a
/// daemon: the route belongs to the daemon, and this file pins what the page
/// does with its answer.
void main() {
  setUp(() {
    calls.clear();
    status = 200;
    body = '{"hosts":[]}';
    installFakeFetch();
  });

  tearDown(restoreFetch);

  test('the daemon comes first, then each tunnel it reports', () async {
    body =
        '{"hosts":[{"name":"laptop","ssh":"me@laptop","state":"live",'
        '"socket":"/tmp/laptop.sock","since":1763300000000},'
        '{"name":"build","ssh":"me@build","state":"down",'
        '"socket":"/tmp/build.sock","lastError":"connection refused",'
        '"since":1763300060000}]}';

    final hosts = await discoverHosts();
    final page = Uri.base.host;

    expect([for (final host in hosts) host.name], [page, 'laptop', 'build']);
    expect(
      [for (final host in hosts) host.kind],
      [HostKind.server, HostKind.server, HostKind.server],
    );
    // The page's own row is the daemon it was served by, reached directly.
    expect(hosts.first.via, isNull);
    expect(hosts.first.destination, isNull);
    // A tunnel is reached through the daemon that reports it, and its
    // destination is the SSH target that daemon opened the tunnel to.
    expect(hosts[1].via, page);
    expect(hosts[1].destination, 'me@laptop');
    expect(hosts[2].via, page);
    expect(
      hosts[2].destination,
      'me@build',
      reason: 'a tunnel that is down is still a host the page lists',
    );
    expect(calls.single, '/api/hosts');
  });

  test('a tunnel naming the page\'s own host adds no second row', () async {
    final page = Uri.base.host;
    body =
        '{"hosts":[{"name":"$page","ssh":"me@$page","state":"live",'
        '"socket":"/tmp/self.sock","since":1763300000000}]}';

    final hosts = await discoverHosts();

    expect([for (final host in hosts) host.name], [page]);
  });

  test('a daemon with no tunnels answers the daemon alone', () async {
    final hosts = await discoverHosts();

    expect([for (final host in hosts) host.name], [Uri.base.host]);
  });

  test('a daemon that fails answers the daemon alone', () async {
    // The route is not answering: the page keeps the daemon it can reach
    // rather than losing the row the page came from.
    status = 500;
    body = '{"error":"no daemon"}';

    final hosts = await discoverHosts();

    expect([for (final host in hosts) host.name], [Uri.base.host]);
  });
}

/// The URLs the fake `fetch` received, oldest first.
final calls = <String>[];

/// The status and body the next answered request carries.
int status = 200;
String body = '{"hosts":[]}';

JSAny? _originalFetch;

/// Replaces the page's `fetch` with one that records the request and answers
/// [status] and [body].
void installFakeFetch() {
  _originalFetch = _globalThis['fetch'];
  _globalThis.setProperty('fetch'.toJS, _fakeFetch.toJS);
}

/// Puts the page's own `fetch` back.
void restoreFetch() {
  final original = _originalFetch;
  if (original != null) {
    _globalThis.setProperty('fetch'.toJS, original);
  }
}

@JS('globalThis')
external JSObject get _globalThis;

/// Answers one fetch call from [status] and [body], recording its URL.
JSPromise<JSAny> _fakeFetch(JSString input, [JSAny? init]) {
  calls.add(input.toDart);
  final response = web.Response(body.toJS, web.ResponseInit(status: status));
  final promise = (_globalThis['Promise'] as JSObject).callMethod<JSObject>(
    'resolve'.toJS,
    response,
  );
  return promise as JSPromise<JSAny>;
}
