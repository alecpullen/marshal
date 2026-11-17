import 'dart:async';

// The implementation is chosen when the app is compiled, not at run time: a
// native build gets the unix socket and HTTPS transports, and a platform with
// neither gets the stub that refuses both.
import 'transport_stub.dart' if (dart.library.io) 'transport_io.dart' as impl;

/// A server profile's bearer token, resolved when a request needs one: a
/// `token_cmd` runs, or the keychain is read, at that moment rather than at
/// startup.
typedef TokenProvider = Future<String> Function();

/// One request to a bridge route.
class Request {
  const Request({
    this.method = 'GET',
    required this.path,
    this.query = const {},
    this.body,
    this.headers = const {},
  });

  final String method;

  /// The route, from the root: `/api/agents`.
  final String path;

  final Map<String, String> query;

  /// The body, encoded as JSON when it is not null.
  final Object? body;

  /// Headers the route reads in addition to the transport's own: a
  /// conditional write names `If-Match` here. A transport sets them after the
  /// ones it needs itself, so a bearer token is never dropped.
  final Map<String, String> headers;
}

/// One answer from a bridge.
class Response {
  const Response({
    required this.status,
    this.headers = const {},
    this.body = '',
  });

  final int status;
  final Map<String, String> headers;
  final String body;

  /// Whether the status is a success.
  bool get ok => status >= 200 && status < 300;
}

/// A bridge answered with a failure status.
class BridgeFailure implements Exception {
  BridgeFailure(this.status, this.message);

  final int status;

  /// The bridge's own words when it sent some, else what the status means.
  final String message;

  @override
  String toString() => 'HTTP $status: $message';
}

/// The bridge refused the request's credentials. It is terminal: the same
/// token cannot start working, so a stream stops rather than reconnecting
/// (F1 decision 6).
class Unauthorized extends BridgeFailure {
  Unauthorized([String message = 'the bridge refused the token'])
    : super(401, message);
}

/// The failure one non-2xx answer becomes.
BridgeFailure bridgeFailure(int status, String body) {
  final message = body.trim();
  if (status == 401) {
    return Unauthorized(
      message.isEmpty ? 'the bridge refused the token' : message,
    );
  }
  return BridgeFailure(
    status,
    message.isEmpty ? 'the bridge answered HTTP $status' : message,
  );
}

/// A transport reaches one bridge. Nothing outside `client/` knows which one
/// is in use.
abstract interface class Transport {
  /// Sends a request and reads its whole answer.
  Future<Response> send(Request request);

  /// Sends a request and answers one element per line of the body, as the
  /// lines arrive rather than once the body is complete. A non-2xx answer
  /// fails the stream with a [BridgeFailure].
  Stream<String> lines(Request request);
}

/// Opens the transport that reaches a local daemon over its unix socket.
Transport unixTransport(String socketPath) => impl.unixTransport(socketPath);

/// Opens the transport that reaches a server profile: its origin, its bearer
/// token, and the CA file its profile names when it names one.
Transport serverTransport(
  Uri base, {
  required TokenProvider token,
  String? caFile,
}) => impl.serverTransport(base, token: token, caFile: caFile);
