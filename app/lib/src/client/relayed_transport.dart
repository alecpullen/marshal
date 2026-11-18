import 'transport.dart';

/// A host's token, or null when none is stored.
typedef HostTokenProvider = Future<String?> Function();

/// The header the relay reads a host's own token from (spec D18).
///
/// The transport sets it after the route's own headers, so a route cannot name
/// a host's credential itself.
const _hostTokenHeader = 'X-Marshal-Host-Token';

/// [relay] with every route under `/api/hosts/<host>` and the host's own
/// token in `X-Marshal-Host-Token` (spec D18).
///
/// The relay carries a request to the host it names and answers with the host's
/// own answer, so everything above this seam is written as though the host were
/// reached directly: a route's path, query, method and body are the original's,
/// and the route's own headers are kept, with the host's token set beside them.
/// The relay's own transport sets its own `Authorization` as it always does, so
/// the bearer a profile holds never reaches a host.
///
/// [host] is the host's name as the relay knows it, and is encoded **once**, as
/// one path segment: a name that contains a slash is `%2F`, never two segments.
/// The request's own path is appended as it is.
///
/// A request made with no token stored is refused here rather than sent, in the
/// words the relay would have used: [Transport.send] answers 401 with them, and
/// [Transport.lines] fails with [Unauthorized]. The relay sees no call at all.
Transport relayedTransport(
  Transport relay, {
  required String host,
  required HostTokenProvider token,
}) => _RelayedTransport(relay, host: host, token: token);

/// A host reached through a relay, written as though it were reached directly.
class _RelayedTransport implements Transport {
  _RelayedTransport(this._relay, {required this.host, required this.token});

  /// The bridge the relay answers on.
  final Transport _relay;

  /// The host's name as the relay knows it.
  final String host;

  /// The host's token, read per request: a sign-in between two requests
  /// applies to the second.
  final HostTokenProvider token;

  /// The words a person reads when no token is stored yet.
  String get _refused => 'sign in to $host first';

  @override
  Future<Response> send(Request request) async {
    final hostToken = await token();
    if (hostToken == null) return Response(status: 401, body: _refused);
    return _relay.send(_forward(request, hostToken));
  }

  @override
  Stream<String> lines(Request request) async* {
    final hostToken = await token();
    // A terminal failure, not a reconnect: the same absent token cannot start
    // working, so the stream stops rather than redialling (F1 decision 6).
    if (hostToken == null) throw Unauthorized(_refused);
    yield* _relay.lines(_forward(request, hostToken));
  }

  /// [request] with the relay's prefix in front of its path and the host's
  /// token beside the route's own headers.
  Request _forward(Request request, String hostToken) => Request(
    method: request.method,
    path: '/api/hosts/${Uri.encodeComponent(host)}${request.path}',
    query: request.query,
    body: request.body,
    headers: {...request.headers, _hostTokenHeader: hostToken},
  );
}
