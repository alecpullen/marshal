import 'dart:async';
import 'dart:convert';
import 'dart:js_interop';

import 'package:web/web.dart' as web;

import 'line_decoder.dart';
import 'transport.dart';

/// The browser transport: same-origin requests to the daemon that served the
/// page, through the operator's proxy.
///
/// It is the app's only way to a daemon in a browser: there is no unix socket
/// there, and a server origin is not reachable from a page the same way.
///
/// The page holds no credential (one-daemon D13): the proxy in front of the
/// daemon is what admits the browser, so nothing here sets an `Authorization`.
/// Most proxies answer an expired session with a redirect rather than a 401,
/// and that redirect is a sign-out all the same: it is never followed to a
/// login page on another origin, so a page that has been signed out reads
/// [Unauthorized] here instead of quietly rendering someone else's login page.
class WebTransport implements Transport {
  WebTransport({this.base});

  /// The origin requests go to. Null is this page's own, which is what the
  /// browser build uses; a test names one.
  final Uri? base;

  /// What a proxy's redirect says: the words a refused request carries when no
  /// daemon answered it.
  static const signedOut = 'signed out: reload to sign in again';

  @override
  Future<Response> send(Request request) async {
    final response = await _fetch(request);
    await _check(response);
    final body = (await response.text().toDart).toDart;
    final type = response.headers.get('content-type');
    // The ETag is what a conditional write names back, so a route that answers
    // one has to reach the client: a settings save re-reads on the value that
    // won (web/bridge/projects.go).
    final etag = response.headers.get('etag');
    return Response(
      status: response.status,
      headers: {'content-type': ?type, 'etag': ?etag},
      body: body,
    );
  }

  @override
  Stream<String> lines(Request request) async* {
    final response = await _fetch(request);
    await _check(response);
    final body = response.body;
    if (body == null) return;

    final reader = body.getReader() as web.ReadableStreamDefaultReader;
    // The decoder spans chunks: a chunk boundary can fall inside a
    // character's UTF-8 bytes, and decoding each chunk on its own would throw
    // on the split character and kill the stream.
    final decoder = LineDecoder();
    try {
      while (true) {
        final result = await reader.read().toDart;
        if (result.done) break;
        final value = result.value;
        if (value == null) continue;
        for (final line in decoder.add((value as JSUint8Array).toDart)) {
          yield line;
        }
      }
      // A last line the body did not terminate is still a line.
      for (final line in decoder.close()) {
        yield line;
      }
    } finally {
      reader.releaseLock();
    }
  }

  /// Sends the request and answers the response, before its body is read.
  Future<web.Response> _fetch(Request request) async {
    final headers = web.Headers();
    // The route's own headers: a conditional write names If-Match here.
    for (final MapEntry(:key, :value) in request.headers.entries) {
      headers.set(key, value);
    }
    final init = web.RequestInit(
      method: request.method,
      headers: headers,
      // A proxy answers an expired session with a redirect to its login page.
      // Following it would fetch that page, on another origin, and read it as
      // the answer: a manual redirect stops here and reads as a sign-out.
      redirect: 'manual',
    );
    if (request.body != null) {
      headers.set('Content-Type', 'application/json');
      init.body = jsonEncode(request.body).toJS;
    }
    return web.window.fetch(_url(request).toJS, init).toDart;
  }

  /// Fails a non-2xx answer, with the daemon's own words when it sent some.
  Future<void> _check(web.Response response) async {
    if (response.ok) return;
    // A manual redirect is opaque: its status is 0 and its body cannot be
    // read, so the words are this transport's own. It is the 401 path all the
    // same — nothing a retry does clears it (one-daemon D13).
    if (response.type == 'opaqueredirect') {
      throw bridgeFailure(401, signedOut);
    }
    final body = (await response.text().toDart).toDart;
    throw bridgeFailure(response.status, body);
  }

  /// The URL one request goes to: the page's own origin unless a base is set.
  String _url(Request request) {
    final query = request.query.isEmpty
        ? ''
        : '?${Uri(queryParameters: request.query).query}';
    final origin = base;
    if (origin == null) return '${request.path}$query';
    final prefix = origin.toString().replaceFirst(RegExp(r'/$'), '');
    return '$prefix${request.path}$query';
  }
}
