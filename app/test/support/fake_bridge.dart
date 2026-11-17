import 'dart:async';
import 'dart:convert';
import 'dart:io';

/// One request the fake bridge received.
class RecordedRequest {
  RecordedRequest({
    required this.method,
    required this.uri,
    required this.headers,
    required this.body,
  });

  final String method;

  /// The path and query, as the request wrote them.
  final String uri;

  /// Header names lower-cased: HTTP's are case-insensitive.
  final Map<String, String> headers;

  final String body;
}

/// A canned answer to one route.
class FakeReply {
  const FakeReply(
    this.status, {
    this.headers = const {},
    this.body = '',
    this.delay,
  });

  final int status;
  final Map<String, String> headers;
  final String body;

  /// How long the answer is held before it is written: a read a test wants
  /// still in flight while it does something else.
  final Duration? delay;
}

/// A body a test writes to, frame by frame: the fleet stream, and anything
/// else that arrives over time. Each frame is written and flushed as it is
/// queued, so a test sees one before the body ends.
class FakeStream {
  var _events = StreamController<String?>();
  var _closed = false;

  /// Queues [frame] for the client.
  void write(String frame) {
    if (_closed) return;
    _events.add(frame);
  }

  /// The body ends cleanly, the way a finished response does.
  Future<void> end() async {
    if (_closed) return;
    _closed = true;
    await _events.close();
  }

  /// The connection dies mid-body: the client sees a dropped stream, not a
  /// finished one.
  Future<void> cut() async {
    if (_closed) return;
    _closed = true;
    _events.add(null);
    await _events.close();
  }

  /// The bridge comes back: the next request for this route gets a fresh body,
  /// and frames written before it arrives are held for it. It is what a host
  /// going live again looks like to a client that is retrying.
  void reopen() {
    if (!_closed) return;
    _events = StreamController<String?>();
    _closed = false;
  }
}

class _Scripted {
  _Scripted(this.stream, this.headers);

  final FakeStream stream;
  final Map<String, String> headers;
}

/// A stand-in for `webbridge`: an in-process server a test starts on a unix
/// socket or on a loopback port, with canned routes, every request recorded,
/// and a stream a test writes to and cuts.
///
/// It is the fixture every model and screen test uses (F1), so a test never
/// reads the developer's daemon, config, keychain or network.
class FakeBridge {
  FakeBridge._(this._server, this.token, this._socketPath);

  final HttpServer _server;

  /// When set, a request without this bearer token is answered 401.
  final String? token;

  final String? _socketPath;
  final _routes = <String, FakeReply>{};
  final _queued = <String, List<FakeReply>>{};
  final _streams = <String, _Scripted>{};
  final _requests = <RecordedRequest>[];

  /// Starts a bridge on the unix socket at [socketPath], which the test owns
  /// and puts in a temporary directory.
  static Future<FakeBridge> onUnixSocket(String socketPath) async {
    final server = await ServerSocket.bind(
      InternetAddress(socketPath, type: InternetAddressType.unix),
      0,
    );
    final bridge = FakeBridge._(HttpServer.listenOn(server), null, socketPath);
    bridge._serve();
    return bridge;
  }

  /// Starts a bridge on a loopback port.
  static Future<FakeBridge> onLoopback({String? token}) async {
    final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    final bridge = FakeBridge._(server, token, null);
    bridge._serve();
    return bridge;
  }

  /// The origin a transport is opened with.
  Uri get base => _socketPath != null
      ? Uri.parse('http://localhost')
      : Uri.parse('http://127.0.0.1:${_server.port}');

  /// The unix socket this bridge listens on, when it is on one.
  String? get socketPath => _socketPath;

  /// Every request received, oldest first.
  List<RecordedRequest> get requests => List.unmodifiable(_requests);

  /// Answers `METHOD PATH` with [reply].
  void reply(String method, String path, FakeReply reply) {
    _routes['$method $path'] = reply;
  }

  /// Answers the next request to `METHOD PATH` with [reply], then falls back
  /// to the route [reply] set. It is how a test scripts an answer that
  /// changes between two requests of the same route — a conditional write
  /// refused with 412 and then accepted.
  void replyOnce(String method, String path, FakeReply reply) =>
      _queued.putIfAbsent('$method $path', () => []).add(reply);

  /// Answers `METHOD PATH` with a body a test writes to, frame by frame.
  FakeStream stream(
    String method,
    String path, {
    Map<String, String> headers = const {'content-type': 'text/event-stream'},
  }) {
    final stream = FakeStream();
    _streams['$method $path'] = _Scripted(stream, headers);
    return stream;
  }

  /// Stops listening and drops every open connection.
  Future<void> close() => _server.close(force: true);

  void _serve() {
    _server.listen(_handle, onError: (Object _) {});
  }

  Future<void> _handle(HttpRequest request) async {
    final response = request.response;
    try {
      final body = await utf8.decoder.bind(request).join();
      final headers = <String, String>{};
      request.headers.forEach((name, values) {
        headers[name.toLowerCase()] = values.join(',');
      });
      _requests.add(
        RecordedRequest(
          method: request.method,
          uri: request.uri.toString(),
          headers: headers,
          body: body,
        ),
      );

      final key = '${request.method} ${request.uri.path}';
      final required = token;
      if (required != null &&
          headers[HttpHeaders.authorizationHeader] != 'Bearer $required') {
        response.statusCode = 401;
        response.headers.contentType = ContentType.json;
        response.write(jsonEncode({'error': 'unauthorized'}));
        await response.close();
        return;
      }

      final scripted = _streams[key];
      if (scripted != null) {
        await _writeStream(scripted, response);
        return;
      }

      // A scripted reply comes first, once, and the route's own answer after
      // it: a conditional write that is refused and then accepted needs two
      // different answers to the same route.
      final queued = _queued[key];
      final reply = queued != null && queued.isNotEmpty
          ? queued.removeAt(0)
          : _routes[key];
      if (reply == null) {
        response.statusCode = 404;
        response.headers.contentType = ContentType.json;
        response.write(jsonEncode({'error': 'no canned route for $key'}));
        await response.close();
        return;
      }
      // A held answer keeps the client waiting, so a test can have one read
      // in flight while it does something else. The reply is looked up when
      // the request arrives, so re-serving the route changes the next
      // request, not this one.
      final delay = reply.delay;
      if (delay != null) await Future<void>.delayed(delay);
      response.statusCode = reply.status;
      reply.headers.forEach(response.headers.set);
      response.write(reply.body);
      await response.close();
    } catch (_) {
      // The client tearing the connection down is what a cut looks like from
      // this side; there is nothing to answer.
    }
  }

  Future<void> _writeStream(_Scripted scripted, HttpResponse response) async {
    response.statusCode = 200;
    scripted.headers.forEach(response.headers.set);
    // Each frame goes out as it is written, rather than when a buffer fills:
    // a test watches a stream arrive, and cuts it mid-flight.
    response.bufferOutput = false;
    var cut = false;
    try {
      await for (final frame in scripted.stream._events.stream) {
        if (frame == null) {
          cut = true;
          continue;
        }
        response.write(frame);
        await response.flush();
      }
    } catch (_) {
      return;
    }
    if (cut) {
      final socket = await response.detachSocket(writeHeaders: false);
      socket.destroy();
      return;
    }
    await response.close();
  }
}
