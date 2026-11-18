@TestOn('browser')
library;

import 'dart:js_interop';
import 'dart:js_interop_unsafe';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/relayed_transport.dart';
import 'package:marshal_app/src/client/token_store_web.dart';
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/client/transport_web.dart';
import 'package:web/web.dart' as web;

/// The browser transport, run in Chrome: the bearer token comes from the
/// browser's storage, a pasted header is normalised, and a 401 is terminal.
///
/// `fetch` is replaced for the duration of each test, so a test never needs a
/// bridge: it records the request the transport made and answers what the
/// test scripted.
void main() {
  /// The host store the relayed tests sign in to, and the key it keeps.
  const host = TokenStoreWeb.forHost('laptop');

  setUp(() {
    const TokenStoreWeb().clear();
    host.clear();
    web.window.localStorage.removeItem(svelteTokenStorageKey);
    calls.clear();
    status = 200;
    body = '{}';
    installFakeFetch();
  });

  tearDown(() {
    restoreFetch();
    const TokenStoreWeb().clear();
    host.clear();
    web.window.localStorage.removeItem(svelteTokenStorageKey);
  });

  test('sends the stored token', () async {
    const TokenStoreWeb().write('tok-123');

    final response = await WebTransport().send(
      const Request(path: '/api/agents'),
    );

    expect(response.status, 200);
    expect(calls.single.url, '/api/agents');
    expect(calls.single.authorization, 'Bearer tok-123');
  });

  test('normalises a pasted header', () async {
    // What a user copies out of a shell: the whole header, not just the token.
    const TokenStoreWeb().write('Authorization: Bearer tok-123');

    await WebTransport().send(const Request(path: '/api/agents'));

    expect(calls.single.authorization, 'Bearer tok-123');

    // The shapes the web UI accepts, in either order.
    expect(normalizeToken('  tok-123  '), 'tok-123');
    expect(normalizeToken('Bearer tok-123'), 'tok-123');
    expect(normalizeToken('authorization: bearer tok-123'), 'tok-123');
    expect(normalizeToken('Bearer'), '');
  });

  test('a conditional write names If-Match beside the token', () async {
    // The browser transport sets the route's headers after its own, so a
    // conditional write names If-Match and the bearer token survives it. A
    // transport that dropped the route's headers would make the write
    // unconditional (web/bridge/projects.go).
    const TokenStoreWeb().write('tok-123');

    await WebTransport().send(
      const Request(
        method: 'PUT',
        path: '/api/projects/settings',
        query: {'root': '/home/u/alpha'},
        body: {'workspace': 'alpha'},
        headers: {'If-Match': '"0123"'},
      ),
    );

    final call = calls.single;
    expect(call.ifMatch, '"0123"');
    expect(call.authorization, 'Bearer tok-123');
    expect(call.url, '/api/projects/settings?root=%2Fhome%2Fu%2Falpha');
  });

  test('401 is Unauthorized', () async {
    status = 401;
    body = 'the bridge refused the token';

    await expectLater(
      WebTransport().send(const Request(path: '/api/agents')),
      throwsA(isA<Unauthorized>()),
    );
  });

  test('a host store keeps its own key', () async {
    // Spec D21: a relayed host's token is kept under
    // `marshal:bridge-token@<name>`, beside the page's own and never on top of
    // it.
    const TokenStoreWeb().write('page-token');
    host.write('host-token');

    expect(
      web.window.localStorage.getItem('marshal:bridge-token@laptop'),
      'host-token',
    );
    expect(const TokenStoreWeb().read(), 'page-token');
    expect(host.read(), 'host-token');

    await WebTransport(tokens: host).send(const Request(path: '/api/agents'));

    expect(calls.single.authorization, 'Bearer host-token');
  });

  test('a host store has no svelte fallback', () {
    // The Svelte Studio's key belongs to the bridge that served the page, and
    // so does this Studio's own: neither may reach a host behind the relay.
    web.window.localStorage.setItem(svelteTokenStorageKey, 'svelte-token');
    const TokenStoreWeb().write('page-token');

    expect(const TokenStoreWeb().read(), 'page-token');
    expect(host.read(), isNull, reason: 'a host store reads its own key alone');

    // Clearing and writing a host's key leaves the page's where it was.
    host.clear();
    expect(host.read(), isNull);
    host.write('host-token');
    expect(const TokenStoreWeb().read(), 'page-token');
  });

  test('a relayed host\'s request carries both tokens', () async {
    // The page's token reaches the relay in the transport's own Authorization,
    // and the host's rides beside it, in the header the relay reads (spec D18,
    // D21).
    const TokenStoreWeb().write('page-token');
    host.write('host-token');

    await relayedTransport(
      WebTransport(),
      host: 'laptop',
      token: () async => host.read(),
    ).send(const Request(path: '/api/agents'));

    final call = calls.single;
    expect(call.url, '/api/hosts/laptop/api/agents');
    expect(call.authorization, 'Bearer page-token');
    expect(call.hostToken, 'host-token');
  });
}

/// One request the fake fetch received.
class FakeCall {
  FakeCall({
    required this.url,
    required this.authorization,
    this.ifMatch,
    this.hostToken,
    this.body,
  });

  final String url;
  final String? authorization;

  /// The `If-Match` the route named, when it named one.
  final String? ifMatch;

  /// The relayed host's own token, when a relayed transport set one.
  final String? hostToken;
  final String? body;
}

/// The requests the fake fetch received, oldest first.
final calls = <FakeCall>[];

/// The status and body the next answered request carries.
int status = 200;
String body = '{}';

JSAny? _originalFetch;

/// Replaces the page's fetch with one that records and answers.
void installFakeFetch() {
  _originalFetch = _globalThis['fetch'];
  _globalThis.setProperty('fetch'.toJS, _fakeFetch.toJS);
}

/// Puts the page's own fetch back.
void restoreFetch() {
  final original = _originalFetch;
  if (original != null) {
    _globalThis.setProperty('fetch'.toJS, original);
  }
}

@JS('globalThis')
external JSObject get _globalThis;

/// Answers one fetch call from [status] and [body], recording the request.
JSPromise<JSAny> _fakeFetch(JSString input, [JSAny? init]) {
  final request = init as web.RequestInit?;
  final headers = request?.headers as web.Headers?;
  final requestBody = request?.body;
  calls.add(
    FakeCall(
      url: input.toDart,
      authorization: headers?.get('authorization'),
      ifMatch: headers?.get('if-match'),
      hostToken: headers?.get('x-marshal-host-token'),
      body: requestBody == null ? null : (requestBody as JSString).toDart,
    ),
  );
  final response = web.Response(body.toJS, web.ResponseInit(status: status));
  final promise = (_globalThis['Promise'] as JSObject).callMethod<JSObject>(
    'resolve'.toJS,
    response,
  );
  return promise as JSPromise<JSAny>;
}
