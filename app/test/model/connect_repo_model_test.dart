import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/connect_repo_model.dart';

/// A transport that answers canned routes and records what was asked, so a
/// model test exercises the client and its routes without a socket.
class _Scripted implements Transport {
  _Scripted(this._replies);

  /// `METHOD /route` to the answer it gets.
  final Map<String, Response> _replies;

  /// Every request, oldest first.
  final requests = <Request>[];

  @override
  Future<Response> send(Request request) async {
    requests.add(request);
    final reply = _replies['${request.method} ${request.path}'];
    return reply ??
        Response(
          status: 404,
          body: jsonEncode({'error': 'no canned route for ${request.path}'}),
        );
  }

  @override
  Stream<String> lines(Request request) => const Stream.empty();
}

/// Connecting a repository from the Studio (F9 B1): the id a URL suggests, the
/// repository the bridge kept, and its refusal in its own words.
void main() {
  Map<String, Object?> repoJson({
    String id = 'acme-marshal',
    String url = 'https://github.com/acme/marshal.git',
  }) => {'id': id, 'url': url, 'ownerId': 'owner-1', 'forge': 'github'};

  test('the id comes from the repository URL', () {
    expect(
      ConnectRepoModel.idFor('https://github.com/acme/marshal.git'),
      'acme-marshal',
    );
    // A trailing slash and a query string name nothing extra.
    expect(
      ConnectRepoModel.idFor('https://github.com/acme/marshal/'),
      'acme-marshal',
    );
    expect(
      ConnectRepoModel.idFor('https://github.com/acme/marshal?ref=main'),
      'acme-marshal',
    );
  });

  test('registering stores the repository', () async {
    final transport = _Scripted({
      'POST /api/repos': Response(status: 201, body: jsonEncode(repoJson())),
    });
    final model = ConnectRepoModel(BridgeClient(transport));
    addTearDown(model.dispose);

    await model.register(
      url: 'https://github.com/acme/marshal.git',
      id: 'acme-marshal',
    );

    expect(model.busy, isFalse);
    expect(model.failure, isNull);
    expect(model.registered!.id, 'acme-marshal');
    final post = transport.requests.single;
    expect(post.method, 'POST');
    expect(post.path, '/api/repos');
    final sent = post.body! as Repo;
    expect(sent.id, 'acme-marshal');
    expect(sent.url, 'https://github.com/acme/marshal.git');
  });

  test('a refusal is reported in the bridge\'s words', () async {
    final transport = _Scripted({
      'POST /api/repos': Response(
        status: 409,
        body: jsonEncode({'error': 'repo id acme-marshal is taken'}),
      ),
    });
    final model = ConnectRepoModel(BridgeClient(transport));
    addTearDown(model.dispose);

    await model.register(
      url: 'https://github.com/acme/marshal.git',
      id: 'acme-marshal',
    );

    expect(model.registered, isNull);
    expect(model.failure, contains('repo id acme-marshal is taken'));
  });
}
