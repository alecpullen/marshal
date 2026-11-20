@TestOn('browser')
library;

import 'dart:js_interop';
import 'dart:js_interop_unsafe';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/client/transport_web.dart';
import 'package:web/web.dart' as web;

/// The browser transport, run in Chrome: requests go to the origin it names,
/// the route's own headers survive, there is no credential to send, a 401 is
/// terminal and a proxy's redirect is a sign-out (one-daemon D13).
///
/// `fetch` is replaced for the duration of each test, so a test never needs a
/// bridge: it records the request the transport made and answers what the
/// test scripted.
void main() {
  /// The origin the tests name: a page of its own never served these tests.
  final base = Uri.parse('http://127.0.0.1:7700');

  setUp(() {
    calls.clear();
    status = 200;
    body = '{}';
    installFakeFetch();
  });

  tearDown(restoreFetch);

  test('a conditional write names If-Match, and no Credential', () async {
    // The route's headers are the transport's own: a conditional write names
    // If-Match and nothing else, because the page holds no credential — the
    // operator's proxy admits it (one-daemon D13).
    await WebTransport(base: base).send(
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
    expect(
      call.authorization,
      isNull,
      reason: 'the page sends no Authorization (one-daemon D13)',
    );
    expect(
      call.redirect,
      'manual',
      reason: 'a proxy\'s redirect is never followed to its login page',
    );
    expect(
      call.url,
      'http://127.0.0.1:7700/api/projects/settings?root=%2Fhome%2Fu%2Falpha',
    );
  });

  test('401 is Unauthorized', () async {
    status = 401;
    body = 'the proxy refused the request';

    await expectLater(
      WebTransport(base: base).send(const Request(path: '/api/agents')),
      throwsA(isA<Unauthorized>()),
    );
  });

  test('a redirect is a sign-out', () async {
    // Most proxies answer an expired session with a redirect to their login
    // page, not a 401 (one-daemon D13). A manual redirect is opaque — status
    // 0, no readable body — so it is read off its type, and it fails the
    // request exactly as a 401 does.
    status = 302;
    body = '';

    await expectLater(
      WebTransport(base: base).send(const Request(path: '/api/agents')),
      throwsA(
        isA<Unauthorized>().having(
          (failure) => failure.message,
          'message',
          'signed out: reload to sign in again',
        ),
      ),
    );
    await expectLater(
      WebTransport(
        base: base,
      ).lines(const Request(path: '/api/events', query: {'stream': 'fleet'})),
      emitsError(isA<Unauthorized>()),
    );
  });
}

/// One request the fake fetch received.
class FakeCall {
  FakeCall({
    required this.url,
    required this.authorization,
    this.ifMatch,
    this.redirect,
    this.body,
  });

  final String url;
  final String? authorization;

  /// The `If-Match` the route named, when it named one.
  final String? ifMatch;

  /// The redirect mode the transport asked for.
  final String? redirect;
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
      redirect: request?.redirect,
      body: requestBody == null ? null : (requestBody as JSString).toDart,
    ),
  );
  // A redirect answered to `redirect: 'manual'` is an opaque redirect in the
  // browser: status 0, type `opaqueredirect`, and no body to read. The fake
  // answers exactly that for every 3xx, so the transport is tested against
  // what a proxy's redirect really looks like.
  final answer = status >= 300 && status < 400
      ? _opaqueRedirect()
      : web.Response(body.toJS, web.ResponseInit(status: status));
  final promise = (_globalThis['Promise'] as JSObject).callMethod<JSObject>(
    'resolve'.toJS,
    answer,
  );
  return promise as JSPromise<JSAny>;
}

/// The response a browser hands to a `redirect: 'manual'` fetch.
JSObject _opaqueRedirect() {
  final response = JSObject();
  response.setProperty('type'.toJS, 'opaqueredirect'.toJS);
  response.setProperty('ok'.toJS, false.toJS);
  response.setProperty('status'.toJS, 0.toJS);
  response.setProperty('statusText'.toJS, ''.toJS);
  response.setProperty('url'.toJS, ''.toJS);
  response.setProperty('redirected'.toJS, false.toJS);
  response.setProperty('body'.toJS, null);
  response.setProperty('headers'.toJS, web.Headers());
  return response;
}
