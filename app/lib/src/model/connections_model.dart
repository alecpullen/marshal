import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import 'section_model.dart';

/// What a forge credential lets Marshal do, worked out from its kind
/// (F7 decision 7). An ssh credential pushes; it cannot open a pull request or
/// read checks, because nothing turns a key into an API token.
@immutable
class ForgePermits {
  const ForgePermits({
    required this.canPush,
    required this.canOpenPullRequests,
    required this.canReadChecks,
  });

  final bool canPush;
  final bool canOpenPullRequests;
  final bool canReadChecks;

  /// What it permits in one line, for the row.
  String get summary => switch ((canPush, canOpenPullRequests, canReadChecks)) {
    (true, true, true) => 'Push, pull requests and checks',
    (true, false, false) =>
      'Push only — cannot open pull requests or read checks',
    (false, false, false) => 'Read only',
    _ => 'Partly permitted',
  };

  /// The same, one permission to a line, for the detail.
  List<String> get lines => [
    canPush ? 'Can push to the repository' : 'Cannot push',
    canOpenPullRequests
        ? 'Can open and comment on pull requests'
        : 'Cannot open pull requests',
    canReadChecks ? 'Can read checks' : 'Cannot read checks',
  ];
}

/// What [kind] of credential permits.
ForgePermits permitsFor(String kind) => switch (kind) {
  // A token is both an API key and a git credential.
  'pat' || 'vault' => const ForgePermits(
    canPush: true,
    canOpenPullRequests: true,
    canReadChecks: true,
  ),
  // An ssh key pushes and nothing more: the forge's API wants a token.
  'ssh' => const ForgePermits(
    canPush: true,
    canOpenPullRequests: false,
    canReadChecks: false,
  ),
  _ => const ForgePermits(
    canPush: false,
    canOpenPullRequests: false,
    canReadChecks: false,
  ),
};

/// How a new forge credential holds its secret, as a person chooses it. The
/// bridge's own kinds are `vault`, `pat` and `ssh`; these say what each one
/// asks of the person.
enum CredentialSource {
  /// A token pasted here and kept in the bridge's secrets (`vault`).
  pastedToken,

  /// A token the daemon reads from an environment variable (`pat`).
  environmentVariable,

  /// A private key file on the daemon's machine (`ssh`).
  sshKey,
}

/// A stored credential a forge can be pointed at.
@immutable
class CredentialChoice {
  const CredentialChoice({
    required this.id,
    required this.kind,
    required this.isSet,
  });

  final String id;
  final String kind;

  /// Whether the secret it names exists now.
  final bool isSet;
}

/// The protocols a provider speaks, as the bridge's config names them.
const providerTypes = <(String, String, String)>[
  (
    'openai_compatible',
    'OpenAI-compatible',
    'LM Studio, vLLM, OpenRouter, OpenAI',
  ),
  ('anthropic', 'Anthropic', 'The Messages API'),
  ('ollama', 'Ollama', 'Ollama\'s own endpoints'),
];

/// One row of Connections: a forge, or a model provider.
enum ConnectionKind { forge, provider }

/// A forge or a provider, as the section draws it: what it is, what it
/// permits, and what it answered when it was last asked.
@immutable
class Connection {
  const Connection({
    required this.kind,
    required this.id,
    required this.name,
    required this.description,
    required this.value,
    this.facts = const [],
    this.permits,
    this.credentialID,
    this.needsAttention = false,
  });

  final ConnectionKind kind;

  /// The repo's id, or the provider's name: what a probe is asked about.
  final String id;

  final String name;
  final String description;

  /// What it says now: what the credential permits, or the provider's health.
  final String value;

  /// The detail's named facts.
  final List<(String, String)> facts;

  /// What a forge's credential permits; null for a provider.
  final ForgePermits? permits;

  /// The stored credential a forge is reached with; null for a provider, and
  /// for a forge with none.
  final String? credentialID;

  /// Something here stops it working: a forge with no credential or one whose
  /// secret is missing, a provider whose last probe failed.
  final bool needsAttention;

  bool get isProvider => kind == ConnectionKind.provider;
}

/// What a probe answered, as the section draws it: the models and their
/// windows, or what went wrong. The section reads this rather than a wire
/// type, because `ui/` reads `model/` and never `client/`.
@immutable
class ProbeAnswer {
  const ProbeAnswer({required this.models, this.error});

  final List<(String, int?)> models;
  final String? error;

  bool get failed => error != null && error!.isNotEmpty;

  int get count => models.length;
}

/// Settings' Connections section (F7 decision 7): the forges Marshal pushes
/// to and what each credential permits, and the model providers with their
/// health and a probe.
class ConnectionsModel extends SectionModel<Connection> {
  ConnectionsModel(this._client);

  /// A model already holding [connections], for a test or the gallery.
  ConnectionsModel.fixture({
    List<Repo> repos = const [],
    List<Credential> credentials = const [],
    ModelsConfig? models,
    Map<String, ProbeResult> probes = const {},
  }) : _client = null {
    _repos = repos;
    _credentials = {for (final one in credentials) one.id: one};
    _config = models;
    _probes.addAll(probes);
    replaceItems(_connections());
  }

  final BridgeClient? _client;

  List<Repo> _repos = const [];
  Map<String, Credential> _credentials = const {};
  ModelsConfig? _config;
  final _probes = <String, ProbeResult>{};

  /// What [id] answered when it was last probed, or null before it was.
  ProbeAnswer? probeOf(String id) {
    final probe = _probes[id];
    if (probe == null) return null;
    return ProbeAnswer(
      models: [
        for (final model in probe.models) (model.id, model.contextWindow),
      ],
      error: probe.error,
    );
  }

  /// True while a probe is in flight, so the button says so.
  bool probing = false;

  @override
  Future<List<Connection>> fetch() async {
    final client = _client;
    if (client == null) return _connections();
    _repos = await client.repos();
    final credentials = await client.credentials();
    _credentials = {for (final one in credentials) one.id: one};
    _config = await client.models();
    return _connections();
  }

  @override
  Object keyOf(Connection item) => '${item.kind.name}/${item.id}';

  @override
  bool isNew(Connection item) => false;

  /// Connections are read here: the rows report what the bridge holds, and
  /// what a client writes for them — a repo, a credential, a provider — is
  /// the bridge's own config.
  @override
  Future<Connection> persist(Connection item) async => item;

  /// Every stored credential, for a forge to be pointed at.
  List<CredentialChoice> get credentialChoices => [
    for (final one in _credentials.values)
      CredentialChoice(id: one.id, kind: one.kind, isSet: one.set),
  ];

  /// Whether this model can write: one without a bridge is a fixture.
  bool get canWrite => _client != null;

  BridgeClient _bridge() =>
      _client ??
      (throw StateError('No bridge is connected, so nothing can be saved'));

  /// Reads what the bridge holds again after a write, so every row says what
  /// is true now.
  Future<void> _refresh() async => replaceItems(await fetch());

  /// Registers [url] under [id]. A refusal throws in the bridge's own words.
  Future<void> connectRepository({
    required String url,
    required String id,
    String forge = 'github',
    String? credentialID,
    String? branch,
  }) async {
    await _bridge().registerRepo(
      Repo(
        id: id,
        url: url,
        ownerID: '',
        forge: forge,
        credRef: credentialID,
        branch: branch,
      ),
    );
    await _refresh();
  }

  /// Points [repoID] at [credentialID], or at none. The repo is registered
  /// again whole, so nothing else about it changes.
  Future<void> useCredential(String repoID, String? credentialID) async {
    final repo = _repos.firstWhere((one) => one.id == repoID);
    await _bridge().registerRepo(
      Repo(
        id: repo.id,
        url: repo.url,
        branch: repo.branch,
        credRef: credentialID,
        ownerID: repo.ownerID,
        forge: repo.forge,
        apiBase: repo.apiBase,
        watch: repo.watch,
        watchLabel: repo.watchLabel,
      ),
    );
    await _refresh();
  }

  /// Forgets a repository. Its credential stays.
  Future<void> removeRepository(String id) async {
    await _bridge().removeRepo(id);
    await _refresh();
  }

  /// Stores a credential named [id], and — for a pasted token — the token in
  /// the bridge's secrets. The token goes one way: nothing reads it back.
  ///
  /// [value] is the token, the environment variable's name or the key file's
  /// path, by [source]. When [repoID] is given the repository is pointed at
  /// the new credential in the same step.
  Future<void> addCredential({
    required String id,
    required CredentialSource source,
    required String value,
    String? user,
    String? repoID,
  }) async {
    final client = _bridge();
    final input = switch (source) {
      CredentialSource.pastedToken => CredentialInput(
        id: id,
        kind: 'vault',
        ref: 'vault:git/$id',
        user: user,
      ),
      CredentialSource.environmentVariable => CredentialInput(
        id: id,
        kind: 'pat',
        envVar: value,
        user: user,
      ),
      CredentialSource.sshKey => CredentialInput(
        id: id,
        kind: 'ssh',
        keyPath: value,
        user: user,
      ),
    };
    if (source == CredentialSource.pastedToken) {
      await client.putSecret('vault:git/$id', value);
    }
    await client.putCredential(input);
    _credentials = {
      ..._credentials,
      id: Credential(id: id, kind: input.kind, set: true),
    };
    if (repoID != null) {
      await useCredential(repoID, id);
    } else {
      await _refresh();
    }
  }

  /// Deletes a credential. The bridge refuses while a repository uses it.
  Future<void> removeCredential(String id) async {
    await _bridge().deleteCredential(id);
    await _refresh();
  }

  /// Adds or replaces the provider [name]. A key, when given, is stored with
  /// it and never read back.
  Future<void> saveProvider({
    required String name,
    required String type,
    required String baseURL,
    String? apiKeyEnv,
    bool toolCalling = true,
    String? key,
  }) async {
    final client = _bridge();
    await client.setProviders(
      ProvidersUpdate(
        providers: {
          name: ProviderWrite(
            type: type,
            baseURL: baseURL,
            apiKeyEnv: apiKeyEnv,
            toolCalling: toolCalling,
          ),
        },
      ),
    );
    if (key != null && key.isNotEmpty) await client.setProviderKey(name, key);
    await _refresh();
  }

  /// Stores a new key for [name]. It goes one way.
  Future<void> setProviderKey(String name, String key) async {
    await _bridge().setProviderKey(name, key);
    await _refresh();
  }

  /// Removes the provider [name] from the user's config.
  Future<void> removeProvider(String name) async {
    await _bridge().setProviders(ProvidersUpdate(providers: {name: null}));
    _probes.remove(name);
    await _refresh();
  }

  /// Asks the provider what models it answers with and puts the answer in
  /// the row. A probe that fails is an answer too: it says what went wrong.
  Future<ProbeResult> probe(Connection connection) async {
    final client = _client;
    if (client == null) {
      return _probes[connection.id] ??
          const ProbeResult(models: [], error: 'Nothing to probe');
    }
    probing = true;
    notifyListeners();
    try {
      final result = await client.probeProvider(connection.id);
      _probes[connection.id] = result;
      replaceItems(_connections());
      return result;
    } finally {
      probing = false;
      notifyListeners();
    }
  }

  /// Whether the bridge already holds the secret [repoID]'s forge signs its
  /// deliveries with (F7's webhook setup, ported from the Svelte client).
  ///
  /// The answer is `true` or `false`, or `null` when the secrets backend
  /// cannot say — it may be switched off, and that is not an error the person
  /// made. It never throws for that case: the section says the state is
  /// unknown and Create secret reports why if issuing one fails.
  Future<bool?> webhookSecretSet(String repoID) async {
    final client = _client;
    if (client == null) return null;
    try {
      final refs = await client.secrets(prefix: 'hooks/');
      final wanted = 'hooks/$repoID';
      return refs.any((ref) {
        final bare = ref.startsWith('vault:') ? ref.substring(6) : ref;
        return bare == wanted;
      });
    } catch (_) {
      return null;
    }
  }

  /// Issues the secret [repoID]'s forge signs its deliveries with, and answers
  /// it the one time the bridge shows it. Without a secret the bridge polls
  /// for changes instead.
  Future<String> createWebhookSecret(String repoID) async {
    final client = _client;
    if (client == null) {
      throw StateError('No bridge is connected, so no secret can be issued');
    }
    return client.issueWebhookSecret(repoID);
  }

  /// What a provider's health says in one line: where its key comes from and
  /// what its last probe found.
  String _health(String name, ProviderView provider) {
    final key = switch (provider.keySource) {
      'config' => 'Key stored',
      'env' => 'Key from ${provider.apiKeyEnv ?? 'the environment'}',
      _ => 'No key',
    };
    final probe = _probes[name];
    if (probe == null) return '$key · not probed';
    final error = probe.error;
    if (error != null && error.isNotEmpty) return '$key · probe failed';
    final count = probe.models.length;
    return '$key · $count ${count == 1 ? 'model' : 'models'}';
  }

  /// Every row: the forges first, then the providers.
  List<Connection> _connections() {
    final rows = <Connection>[];
    for (final repo in _repos) {
      final credential = repo.credRef == null
          ? null
          : _credentials[repo.credRef];
      final kind = credential?.kind ?? 'none';
      final permits = permitsFor(kind);
      final credentialWords = credential == null
          ? 'no credential'
          : '${credential.kind} credential ${credential.id}';
      rows.add(
        Connection(
          kind: ConnectionKind.forge,
          id: repo.id,
          name: repo.id,
          description: '${repo.forge ?? 'git'} · $credentialWords',
          value: switch (credential) {
            null => 'No credential — cannot push',
            Credential(set: false) => 'Credential secret missing',
            _ => permits.summary,
          },
          permits: permits,
          credentialID: credential?.id,
          needsAttention: credential == null || !credential.set,
          facts: [
            ('Repository', repo.url),
            if (repo.branch != null) ('Branch', repo.branch!),
            if (repo.forge != null) ('Forge', repo.forge!),
            (
              'Credential',
              credential == null
                  ? 'None: nothing can push to this repository'
                  : '${credential.kind} · ${credential.id}'
                        '${credential.set ? '' : ' · its secret is missing'}',
            ),
            for (final line in permits.lines) ('', line),
          ],
        ),
      );
    }
    final config = _config;
    if (config != null) {
      final names = config.providers.keys.toList()..sort();
      for (final name in names) {
        final provider = config.providers[name]!;
        final probe = _probes[name];
        rows.add(
          Connection(
            kind: ConnectionKind.provider,
            id: name,
            name: name,
            description: '${provider.type} · ${provider.baseURL}',
            value: _health(name, provider),
            needsAttention: probe?.error?.isNotEmpty ?? false,
            facts: [
              ('Type', provider.type),
              ('Base URL', provider.baseURL),
              (
                'Key',
                switch (provider.keySource) {
                  'config' => 'Stored in the user config',
                  'env' =>
                    'Read from ${provider.apiKeyEnv ?? 'the environment'}',
                  _ => 'None set',
                },
              ),
              ('Tool calling', provider.toolCalling ? 'Yes' : 'No'),
            ],
          ),
        );
      }
    }
    return rows;
  }
}
