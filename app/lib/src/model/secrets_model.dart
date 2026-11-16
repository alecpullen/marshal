import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import 'section_model.dart';

/// What one row of Secrets is (F7 decision 7).
enum SecretRowKind {
  /// The backend that holds the secrets and whether it answers.
  status,

  /// One stored reference.
  secret,

  /// A reference a person is about to write.
  newSecret,
}

/// A failure the person made, in words: an empty reference, or an empty value.
@immutable
class SecretValueError implements Exception {
  const SecretValueError(this.message);

  final String message;

  @override
  String toString() => message;
}

/// One row of Secrets: the backend, a reference, or a reference being created.
///
/// A row never holds a value for longer than the save that writes it: the
/// bridge has no route that answers one back, so nothing here could show one
/// even if it wanted to.
@immutable
class SecretRow {
  const SecretRow({
    required this.kind,
    required this.ref,
    required this.name,
    required this.description,
    required this.value,
    this.usedBy = const [],
  });

  final SecretRowKind kind;

  /// The reference, as `vault:<path>`; empty for one being created.
  final String ref;

  final String name;
  final String description;

  /// What the row says now, or what was typed into it.
  final String value;

  /// The credentials that name this reference, by id.
  final List<String> usedBy;

  SecretRow withRef(String ref) => SecretRow(
    kind: kind,
    ref: ref,
    name: ref,
    description: description,
    value: value,
    usedBy: usedBy,
  );

  SecretRow withValue(String value) => SecretRow(
    kind: kind,
    ref: ref,
    name: name,
    description: description,
    value: value,
    usedBy: usedBy,
  );
}

/// Settings' Secrets section (F7 decision 7): every reference the backend
/// holds, where each is used, and a value that can be written and never read
/// back.
class SecretsModel extends SectionModel<SecretRow> {
  SecretsModel(this._client);

  /// A model already holding its rows, for a test or the gallery.
  SecretsModel.fixture({
    SecretsStatus? status,
    List<String> refs = const [],
    List<Credential> credentials = const [],
  }) : _client = null {
    _status = status ?? const SecretsStatus(backend: 'local', healthy: true);
    _refs = refs;
    _credentials = credentials;
    replaceItems(_rows());
  }

  final BridgeClient? _client;

  SecretsStatus? _status;
  List<String> _refs = const [];
  List<Credential> _credentials = const [];

  /// The references a value was written for in this window: no route says a
  /// stored value exists, so this is the only thing the row can report.
  final _written = <String>{};

  @override
  Future<List<SecretRow>> fetch() async {
    final client = _client;
    if (client == null) return _rows();
    _status = await client.secretsStatus();
    _refs = await client.secrets();
    _credentials = await client.credentials();
    return _rows();
  }

  @override
  Object keyOf(SecretRow item) => '${item.kind.name}/${item.ref}';

  @override
  bool isNew(SecretRow item) => item.kind == SecretRowKind.newSecret;

  /// Writes the value the person typed to the row's reference, and answers
  /// the row it makes. The value is not kept: what a row reports afterwards is
  /// that one was written.
  @override
  Future<SecretRow> persist(SecretRow item) async {
    if (item.kind == SecretRowKind.status) return item;
    final ref = item.ref.trim();
    if (ref.isEmpty) {
      throw const SecretValueError(
        'A secret needs a reference, as vault:<path>',
      );
    }
    if (item.value.isEmpty) {
      throw const SecretValueError('A secret needs a value');
    }
    final client = _client;
    if (client != null) await client.putSecret(ref, item.value);
    _written.add(ref);
    if (!_refs.contains(ref)) _refs = [..._refs, ref]..sort();
    replaceItems(_rows());
    return _rows().firstWhere((row) => row.ref == ref);
  }

  /// The reference a person starts a new secret with.
  static SecretRow blank() => const SecretRow(
    kind: SecretRowKind.newSecret,
    ref: '',
    name: '',
    description: 'A new reference and the value it holds',
    value: '',
  );

  /// Where [ref] is used: the credentials that name it.
  List<String> _usesOf(String ref) => [
    for (final credential in _credentials)
      if (credential.ref == ref || credential.ref == 'vault:$ref')
        credential.id,
  ];

  /// What a reference's row says about where it is used.
  String _useWords(String ref) {
    final uses = _usesOf(ref);
    return uses.isEmpty
        ? 'No credential names it'
        : 'Used by credential ${uses.join(', ')}';
  }

  List<SecretRow> _rows() {
    final status = _status;
    return [
      if (status != null)
        SecretRow(
          kind: SecretRowKind.status,
          ref: '',
          name: 'Secrets backend',
          description: 'Where the values are kept',
          value: status.healthy
              ? '${status.backend} · answering'
              : '${status.backend} · not answering',
        ),
      for (final ref in _refs)
        SecretRow(
          kind: SecretRowKind.secret,
          ref: ref,
          name: ref,
          description: _useWords(ref),
          value: _written.contains(ref) ? 'Written' : 'Value not readable',
          usedBy: _usesOf(ref),
        ),
    ];
  }
}
