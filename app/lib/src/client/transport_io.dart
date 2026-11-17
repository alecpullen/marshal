import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'transport.dart';

/// The local daemon's transport: every request goes down [socketPath], whatever
/// host the base URL names (internal/daemon/client.go does the same).
Transport unixTransport(String socketPath) => _UnixTransport(socketPath);

/// A server profile's transport: [base] is its origin, and every request
/// carries `Authorization: Bearer` the token [token] resolves. [caFile], when
/// the profile names one, joins the trusted roots for the handshake.
Transport serverTransport(
  Uri base, {
  required TokenProvider token,
  String? caFile,
}) => _ServerTransport(base, token: token, caFile: caFile);

/// The URI one request goes to.
Uri _uri(Uri base, Request request) => base.replace(
  path: request.path,
  queryParameters: request.query.isEmpty ? null : request.query,
);

/// Sets the headers and the body a request carries.
HttpClientRequest _writeBody(HttpClientRequest httpRequest, Request request) {
  // The route's own headers, after the transport's: a conditional write names
  // If-Match here, and the bearer token above is left alone.
  for (final MapEntry(:key, :value) in request.headers.entries) {
    httpRequest.headers.set(key, value);
  }
  if (request.body != null) {
    httpRequest.headers.contentType = ContentType.json;
    for (final entry in request.headers.entries) {
      httpRequest.headers.set(entry.key, entry.value);
    }
    httpRequest.write(jsonEncode(request.body));
  }
  return httpRequest;
}

/// The answer's headers as a plain map, each name once.
Map<String, String> _headers(HttpClientResponse response) {
  final headers = <String, String>{};
  response.headers.forEach((name, values) {
    headers[name] = values.join(',');
  });
  return headers;
}

/// Reads a non-2xx answer and fails with it. A success is left to the caller.
Future<void> _check(HttpClientResponse response) async {
  if (response.statusCode >= 200 && response.statusCode < 300) {
    return;
  }
  final body = await response.transform(utf8.decoder).join();
  throw bridgeFailure(response.statusCode, body);
}

/// Reads a whole answer.
Future<Response> _read(HttpClientResponse response) async {
  await _check(response);
  return Response(
    status: response.statusCode,
    headers: _headers(response),
    body: await response.transform(utf8.decoder).join(),
  );
}

/// Reads an answer one line at a time, as its bytes arrive.
Stream<String> _readLines(HttpClientResponse response) async* {
  await _check(response);
  yield* response.transform(utf8.decoder).transform(const LineSplitter());
}

class _UnixTransport implements Transport {
  _UnixTransport(this.socketPath);

  final String socketPath;

  /// A client whose every connection is the unix socket.
  HttpClient _client() => HttpClient()
    ..connectionFactory = (uri, proxyHost, proxyPort) => Socket.startConnect(
      InternetAddress(socketPath, type: InternetAddressType.unix),
      0,
    );

  /// The daemon serves one origin over the socket, so the host is arbitrary.
  Uri get _base => Uri.parse('http://localhost');

  Future<HttpClientResponse> _open(HttpClient client, Request request) async {
    final httpRequest = await client.openUrl(
      request.method,
      _uri(_base, request),
    );
    return _writeBody(httpRequest, request).close();
  }

  @override
  Future<Response> send(Request request) async {
    final client = _client();
    try {
      return await _read(await _open(client, request));
    } finally {
      client.close();
    }
  }

  @override
  Stream<String> lines(Request request) async* {
    final client = _client();
    try {
      yield* _readLines(await _open(client, request));
    } finally {
      client.close();
    }
  }
}

class _ServerTransport implements Transport {
  _ServerTransport(this.base, {required this.token, this.caFile});

  final Uri base;
  final TokenProvider token;
  final String? caFile;

  HttpClient _client() {
    if (base.scheme != 'https') {
      return HttpClient();
    }
    final context = SecurityContext(withTrustedRoots: true);
    final file = caFile;
    if (file != null) {
      context.setTrustedCertificates(file);
    }
    return HttpClient(context: context);
  }

  Future<HttpClientResponse> _open(HttpClient client, Request request) async {
    final httpRequest = await client.openUrl(
      request.method,
      _uri(base, request),
    );
    // Resolved here, per request: a token_cmd runs when the bridge is asked,
    // not when the client is built.
    httpRequest.headers.set(
      HttpHeaders.authorizationHeader,
      'Bearer ${await token()}',
    );
    return _writeBody(httpRequest, request).close();
  }

  @override
  Future<Response> send(Request request) async {
    final client = _client();
    try {
      return await _read(await _open(client, request));
    } finally {
      client.close();
    }
  }

  @override
  Stream<String> lines(Request request) async* {
    final client = _client();
    try {
      yield* _readLines(await _open(client, request));
    } finally {
      client.close();
    }
  }
}
