import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import 'age.dart';
import 'section_model.dart';

/// Which environment and agent the Network section is showing (F7 decision 7):
/// the two filters the design names.
@immutable
class NetworkScope {
  const NetworkScope({this.environment = '', this.agent = ''});

  /// A workspace or environment name; empty is every one.
  final String environment;

  /// An agent id; empty is every one.
  final String agent;

  bool get isEvery => environment.isEmpty && agent.isEmpty;

  /// The scope in the design system's words: "go-service · a-1".
  String get words => [
    environment.isEmpty ? 'every environment' : environment,
    agent.isEmpty ? 'every agent' : agent,
  ].join(' · ');

  NetworkScope withEnvironment(String environment) =>
      NetworkScope(environment: environment, agent: agent);

  NetworkScope withAgent(String agent) =>
      NetworkScope(environment: environment, agent: agent);
}

/// What one row of Network is.
enum NetworkRowKind { filter, host }

/// One row of Network: the filter, or one egress host.
@immutable
class NetworkRow {
  const NetworkRow({
    required this.kind,
    required this.id,
    required this.name,
    required this.description,
    required this.value,
    this.facts = const [],
  });

  final NetworkRowKind kind;
  final String id;
  final String name;
  final String description;

  /// What the row says now: the scope, or the host's request count and when it
  /// was last seen.
  final String value;

  /// The detail's named facts.
  final List<(String, String)> facts;
}

/// Settings' Network section (F7 decision 7): the egress hosts with the rule
/// that covers each, their request counts and when they were last seen,
/// filtered by environment and agent.
///
/// A blocked request is not a row here: it is an ask of kind `network`, which
/// the Inbox and the agent's thread carry (decision 7).
class NetworkModel extends SectionModel<NetworkRow> {
  NetworkModel(this._client, {DateTime Function()? now})
    : _now = now ?? DateTime.now;

  /// A model already holding its rows, for a test or the gallery.
  NetworkModel.fixture({
    List<NetHostRow> hosts = const [],
    List<String> environments = const [],
    List<String> agents = const [],
    NetworkScope scope = const NetworkScope(),
    DateTime Function()? now,
  }) : _client = null,
       _now = now ?? DateTime.now {
    _hosts = NetHosts(processMode: true, rows: hosts);
    _environments = environments;
    _agents = agents;
    _scope = scope;
    replaceItems(_rows());
  }

  final BridgeClient? _client;
  final DateTime Function() _now;

  NetHosts? _hosts;
  List<String> _environments = const [];
  List<String> _agents = const [];
  NetworkScope _scope = const NetworkScope();

  /// The filter in force.
  NetworkScope get scope => _scope;

  /// The environments and agents the filter offers.
  List<String> get environments => _environments;
  List<String> get agents => _agents;

  /// What the egress proxy is doing, when the bridge says it is down.
  String? get egressError => _hosts?.egressError;

  @override
  Future<List<NetworkRow>> fetch() async {
    final client = _client;
    if (client != null) {
      _hosts = await client.networkHosts(
        workspace: _scope.environment,
        agent: _scope.agent,
      );
      final agents = await client.networkAgents();
      _environments = {
        for (final row in _hosts?.rows ?? const <NetHostRow>[])
          if (row.workspace != null && row.workspace!.isNotEmpty)
            row.workspace!,
      }.toList()..sort();
      _agents = {for (final row in agents) row.agentID}.toList()..sort();
    }
    return _rows();
  }

  @override
  Object keyOf(NetworkRow item) => item.id;

  @override
  bool isNew(NetworkRow item) => false;

  /// Nothing here is written: the rows report what the proxy logged, and the
  /// decisions are the ask's.
  @override
  Future<NetworkRow> persist(NetworkRow item) async => item;

  /// Shows another environment or agent, and reads the hosts again.
  Future<void> filterTo(NetworkScope next) async {
    if (next.environment == _scope.environment && next.agent == _scope.agent) {
      return;
    }
    _scope = next;
    await load();
  }

  List<NetworkRow> _rows() {
    final hosts = _hosts?.rows ?? const <NetHostRow>[];
    return [
      NetworkRow(
        kind: NetworkRowKind.filter,
        id: 'filter',
        name: 'Filter',
        description: 'Which environment and agent the hosts are for',
        value: _scope.words,
      ),
      for (final row in hosts)
        NetworkRow(
          kind: NetworkRowKind.host,
          id: '${row.agentID ?? ''}/${row.host}',
          name: row.host,
          description: [
            row.rule,
            if (row.workspace != null && row.workspace!.isNotEmpty)
              row.workspace!,
            if (row.agentID != null && row.agentID!.isNotEmpty) row.agentID!,
          ].join(' · '),
          value:
              '${row.requests} '
              '${row.requests == 1 ? 'request' : 'requests'} · '
              '${relativeAge(DateTime.fromMillisecondsSinceEpoch(row.lastSeen), _now())}',
          facts: [
            ('Rule', row.rule),
            ('Requests', '${row.requests}, ${row.blocked} blocked'),
            ('Bytes', '${row.bytesUp} up · ${row.bytesDown} down'),
            ('Decision', row.decision),
            if (row.injected ?? false) ('Credential', 'Injected by the proxy'),
            (
              'Last seen',
              relativeAge(
                DateTime.fromMillisecondsSinceEpoch(row.lastSeen),
                _now(),
              ),
            ),
          ],
        ),
    ];
  }
}
