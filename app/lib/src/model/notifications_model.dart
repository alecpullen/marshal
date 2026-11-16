import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import 'section_model.dart';

/// What one row of Notifications is (F7 decision 7).
enum NotifyRowKind {
  /// One webhook Marshal posts to.
  webhook,

  /// The action that posts a test to all of them.
  test,
}

/// One row of Notifications.
@immutable
class NotifyRow {
  const NotifyRow({
    required this.kind,
    required this.id,
    required this.name,
    required this.description,
    required this.value,
    this.facts = const [],
  });

  final NotifyRowKind kind;
  final String id;
  final String name;
  final String description;
  final String value;
  final List<(String, String)> facts;
}

/// Settings' Notifications section (F7 decision 7): where Marshal reaches the
/// user — its webhooks — and Send a test, which proves a webhook can be
/// reached.
///
/// Signal and Element are planned rows (decision 9): the bridge posts to
/// webhooks only, so a row for a messenger would be drawing a capability that
/// does not exist.
class NotificationsModel extends SectionModel<NotifyRow> {
  NotificationsModel(this._client);

  /// A model already holding its rows, for a test or the gallery.
  NotificationsModel.fixture({
    List<NotifyWebhook> webhooks = const [],
    int? sent,
  }) : _client = null {
    _webhooks = webhooks;
    _sent = sent;
    replaceItems(_rows());
  }

  final BridgeClient? _client;

  List<NotifyWebhook> _webhooks = const [];
  int? _sent;

  /// How many webhooks the last test was queued for, or null before one.
  int? get sent => _sent;

  @override
  Future<List<NotifyRow>> fetch() async {
    final client = _client;
    if (client == null) return _rows();
    final config = await client.notifications();
    _webhooks = config.webhooks;
    return _rows();
  }

  @override
  Object keyOf(NotifyRow item) => item.id;

  @override
  bool isNew(NotifyRow item) => false;

  /// A webhook is edited in the bridge's own configuration: this section
  /// reports what it holds and proves it works.
  @override
  Future<NotifyRow> persist(NotifyRow item) async => item;

  /// Posts a test to every webhook and answers how many it was queued for.
  Future<int> sendTest() async {
    final client = _client;
    if (client == null) {
      _sent = _webhooks.length;
      replaceItems(_rows());
      return _webhooks.length;
    }
    final result = await client.testNotifications();
    _sent = result.sent;
    replaceItems(_rows());
    return result.sent;
  }

  List<NotifyRow> _rows() => [
    for (final webhook in _webhooks)
      NotifyRow(
        kind: NotifyRowKind.webhook,
        id: 'webhook/${webhook.id}',
        name: webhook.url,
        description: webhook.events.isEmpty
            ? 'Every event'
            : webhook.events.join(', '),
        value: webhook.secretRef == null
            ? 'Unsigned'
            : 'Signed with ${webhook.secretRef}',
        facts: [
          ('URL', webhook.url),
          ('Events', webhook.events.join(', ')),
          if (webhook.secretRef != null) ('Signing secret', webhook.secretRef!),
        ],
      ),
    NotifyRow(
      kind: NotifyRowKind.test,
      id: 'test',
      name: 'Test a webhook',
      description: 'Send a test posts one notification to every webhook above',
      value: _sent == null
          ? 'Not sent yet'
          : 'Sent to $_sent ${_sent == 1 ? 'webhook' : 'webhooks'}',
    ),
  ];
}
