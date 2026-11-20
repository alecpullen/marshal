import 'transport.dart';

/// [relay] with every route under `/api/hosts/<host>`.
///
/// The daemon that served the page forwards the request to the host's tunnel
/// (one-daemon D10) and answers with the host's own answer, so everything
/// above this seam is written as though the host were reached directly: a
/// route's path, query, method and body are the original's, and the route's
/// own headers are kept. Nothing is added to the request — the page holds no
/// credential, its own or the host's (D13), and a host that refuses is the
/// proxy's answer, carried back unchanged.
///
/// [host] is the host's name as the daemon knows it, and is encoded **once**,
/// as one path segment: a name that contains a slash is `%2F`, never two
/// segments. The request's own path is appended as it is.
Transport relayedTransport(Transport relay, {required String host}) =>
    _RelayedTransport(relay, host: host);

/// A host reached through a relay, written as though it were reached directly.
class _RelayedTransport implements Transport {
  _RelayedTransport(this._relay, {required this.host});

  /// The daemon's transport, which carries the request to the host's tunnel.
  final Transport _relay;

  /// The host's name as the daemon knows it.
  final String host;

  @override
  Future<Response> send(Request request) => _relay.send(_forward(request));

  @override
  Stream<String> lines(Request request) => _relay.lines(_forward(request));

  /// [request] with the relay's prefix in front of its path, every other part
  /// of it unchanged.
  Request _forward(Request request) => Request(
    method: request.method,
    path: '/api/hosts/${Uri.encodeComponent(host)}${request.path}',
    query: request.query,
    body: request.body,
    headers: request.headers,
  );
}
