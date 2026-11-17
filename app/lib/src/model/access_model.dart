import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import 'age.dart';
import 'section_model.dart';

/// What one row of Access is (F7 decision 7).
enum AccessRowKind {
  /// The Studio's own bearer token.
  token,

  /// Where the MCP clients live: Library's MCP tab, not a second list here.
  mcp,

  /// One status link.
  link,

  /// A link a person is about to make.
  newLink,
}

/// A failure of the person's own making: a link with no agent, or a lifetime
/// outside the bridge's bounds.
@immutable
class AccessError implements Exception {
  const AccessError(this.message);

  final String message;

  @override
  String toString() => message;
}

/// One row of Access.
@immutable
class AccessRow {
  const AccessRow({
    required this.kind,
    required this.id,
    required this.name,
    required this.description,
    required this.value,
    this.agentID = '',
    this.hours = 24,
    this.link,
  });

  final AccessRowKind kind;
  final String id;
  final String name;
  final String description;
  final String value;

  /// The agent a link is for, or an agent a new link is about.
  final String agentID;

  /// How long a new link lasts, in hours.
  final int hours;

  /// The link's own record, for a row that has one.
  final StatusLink? link;

  AccessRow withAgent(String agentID) => AccessRow(
    kind: kind,
    id: id,
    name: agentID,
    description: description,
    value: value,
    agentID: agentID,
    hours: hours,
    link: link,
  );

  AccessRow withHours(int hours) => AccessRow(
    kind: kind,
    id: id,
    name: name,
    description: description,
    value: value,
    agentID: agentID,
    hours: hours,
    link: link,
  );
}

/// Settings' Access section (F7 decision 7): the Studio's token, where the MCP
/// clients are, and the status links — made, listed, revoked.
///
/// The plan's decision: Access links to Library's MCP tab rather than listing
/// the clients twice, so its row says where they are and what they hold.
class AccessModel extends SectionModel<AccessRow> {
  AccessModel(this._client, {this.tokenHeld = false, DateTime Function()? now})
    : _now = now ?? DateTime.now;

  /// A model already holding its rows, for a test or the gallery.
  AccessModel.fixture({
    this.tokenHeld = true,
    List<StatusLink> links = const [],
    DateTime Function()? now,
  }) : _client = null,
       _now = now ?? DateTime.now {
    _links = links;
    replaceItems(_rows());
  }

  final BridgeClient? _client;
  final DateTime Function() _now;

  /// Whether this Studio holds a bearer token for its host.
  final bool tokenHeld;

  List<StatusLink> _links = const [];

  /// The URL of the link made in this window, which is the only time any route
  /// gives it out.
  StatusLinkCreated? _made;

  /// The link made in this window, and its URL — shown once.
  StatusLinkCreated? get made => _made;

  @override
  Future<List<AccessRow>> fetch() async {
    final client = _client;
    if (client == null) return _rows();
    _links = await client.statusLinks();
    return _rows();
  }

  @override
  Object keyOf(AccessRow item) => '${item.kind.name}/${item.id}';

  @override
  bool isNew(AccessRow item) => item.kind == AccessRowKind.newLink;

  /// Makes the link the row describes and answers it, with the one URL the
  /// bridge will ever give out.
  @override
  Future<AccessRow> persist(AccessRow item) async {
    if (item.kind != AccessRowKind.newLink) return item;
    final agent = item.agentID.trim();
    if (agent.isEmpty) {
      throw const AccessError('A status link needs an agent');
    }
    if (item.hours < 1 || item.hours > 720) {
      throw const AccessError('A status link lasts between 1 and 720 hours');
    }
    final client = _client;
    if (client == null) {
      throw const AccessError('Nothing to make the link with');
    }
    final made = await client.createStatusLink(agent, ttlHours: item.hours);
    _made = made;
    await load();
    return _rows().firstWhere((row) => row.id == 'link/${made.id}');
  }

  /// Revokes [row]'s link: its URL stops working at once.
  Future<void> revoke(AccessRow row) async {
    final client = _client;
    if (client == null) return;
    await client.revokeStatusLink(row.id.replaceFirst('link/', ''));
    await load();
  }

  /// Makes a status link for [agentID] — the thread header's Share — and
  /// answers the one URL it has.
  Future<StatusLinkCreated> share(String agentID, {int hours = 24}) async {
    final client = _client;
    if (client == null) {
      throw const AccessError('Nothing to make the link with');
    }
    final made = await client.createStatusLink(agentID, ttlHours: hours);
    _made = made;
    await load();
    return made;
  }

  /// The row a person starts a new link with.
  static AccessRow blank() => const AccessRow(
    kind: AccessRowKind.newLink,
    id: 'new',
    name: '',
    description: 'An agent, and how long the link lasts',
    value: '',
  );

  List<AccessRow> _rows() {
    final now = _now();
    return [
      AccessRow(
        kind: AccessRowKind.token,
        id: 'token',
        name: 'Studio token',
        description: 'What this Studio authenticates with',
        value: tokenHeld ? 'A token is held' : 'No token',
      ),
      const AccessRow(
        kind: AccessRowKind.mcp,
        id: 'mcp',
        name: 'MCP clients',
        description:
            'Library’s MCP tab lists them, with their tokens and their '
            'factory grants',
        value: 'Library › MCP',
      ),
      for (final link in _links)
        AccessRow(
          kind: AccessRowKind.link,
          id: 'link/${link.id}',
          name: link.agentID,
          description: link.revokedAt == null
              ? 'Expires in ${relativeAge(DateTime.tryParse(link.expiresAt) ?? now, now)}'
              : 'Revoked',
          value: link.revokedAt == null
              ? 'Live'
              : 'Revoked ${relativeAge(DateTime.tryParse(link.revokedAt!) ?? now, now)} ago',
          agentID: link.agentID,
          link: link,
        ),
    ];
  }
}
