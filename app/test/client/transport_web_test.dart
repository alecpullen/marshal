@TestOn('browser')
library;

import 'dart:js_interop';
import 'dart:js_interop_unsafe';

import 'package:flutter_test/flutter_test.dart';
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
  setUp(() {
    const TokenStoreWeb().clear();
    calls.clear();
    status = 200;
    body = '{}';
    installFakeFetch();
  });

  tearDown(() {
    restoreFetch();
    const TokenStoreWeb().clear();
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

  test('401 is Unauthorized', () async {
    status = 401;
    body = 'the bridge refused the token';

    await expectLater(
      WebTransport().send(const Request(path: '/api/agents')),
      throwsA(isA<Unauthorized>()),
    );
  });
}

/// One request the fake fetch received.
class FakeCall {
  FakeCall({required this.url, required this.authorization, this.body});

  final String url;
  final String? authorization;
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
