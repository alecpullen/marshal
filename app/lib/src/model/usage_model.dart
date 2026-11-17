import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import 'age.dart';
import 'section_model.dart';

/// What one row of Usage and audit is (F7 decision 7).
enum UsageRowKind {
  /// The grouping, the window and the audit kind.
  filter,

  /// The window's totals.
  spend,

  /// One entry of the series.
  series,

  /// One audit record.
  audit,

  /// The disk, and the prune that frees it.
  disk,
}

/// One row of Usage and audit.
@immutable
class UsageRow {
  const UsageRow({
    required this.kind,
    required this.id,
    required this.name,
    required this.description,
    required this.value,
    this.facts = const [],
  });

  final UsageRowKind kind;
  final String id;
  final String name;
  final String description;
  final String value;
  final List<(String, String)> facts;
}

/// The groupings the usage route groups by.
const usageGroupings = <(String, String)>[
  ('day', 'Day by day'),
  ('project', 'By project'),
  ('role', 'By role'),
  ('model', 'By model'),
];

/// The windows the usage route reports over.
const usageRanges = <(String, String)>[
  ('7d', 'The last 7 days'),
  ('30d', 'The last 30 days'),
];

/// Settings' Usage and audit section (F7 decision 7): what the fleet spent by
/// project, role or model, the audit log, and the disk with the prune that
/// frees it.
///
/// It is read-only apart from the filter and the prune: the ledger and the
/// audit log are the bridge's own records.
class UsageModel extends SectionModel<UsageRow> {
  UsageModel(this._client, {DateTime Function()? now})
    : _now = now ?? DateTime.now;

  /// A model already holding its rows, for a test or the gallery.
  UsageModel.fixture({
    UsageReport? report,
    List<AuditEvent> audit = const [],
    DiskStatus? disk,
    DateTime Function()? now,
  }) : _client = null,
       _now = now ?? DateTime.now {
    _report = report;
    _events = audit;
    _disk = disk;
    replaceItems(_rows());
  }

  final BridgeClient? _client;
  final DateTime Function() _now;

  UsageReport? _report;
  List<AuditEvent> _events = const [];
  DiskStatus? _disk;
  String _range = '7d';
  String _by = 'project';
  String _auditKind = '';
  PruneResult? _pruned;

  /// The window and grouping in force.
  String get range => _range;
  String get by => _by;

  /// The audit kind in force; empty is every kind.
  String get auditKind => _auditKind;

  /// What the last prune freed, or null before one ran.
  PruneResult? get pruned => _pruned;

  /// The series the section's chart draws: one bar per key, by cost.
  List<(String, double)> get chartSeries => [
    for (final point in _report?.series ?? const <UsagePoint>[])
      (point.key, point.costUSD),
  ];

  /// Every audit kind the log holds, sorted, for the filter.
  List<String> get auditKinds =>
      {for (final event in _events) event.event}.toList()..sort();

  @override
  Future<List<UsageRow>> fetch() async {
    final client = _client;
    if (client == null) return _rows();
    _report = await client.usage(range: _range, by: _by);
    _events = await client.audit();
    _disk = await client.disk();
    return _rows();
  }

  @override
  Object keyOf(UsageRow item) => item.id;

  @override
  bool isNew(UsageRow item) => false;

  /// Nothing on a row is written: the filter reads another view, the prune is
  /// the section's own action, and the ledger is the bridge's.
  @override
  Future<UsageRow> persist(UsageRow item) async => item;

  /// Groups the report another way and reads it again.
  Future<void> groupBy(String by) async {
    if (by == _by) return;
    _by = by;
    await load();
  }

  /// Shows another window and reads it again.
  Future<void> setRange(String range) async {
    if (range == _range) return;
    _range = range;
    await load();
  }

  /// Narrows the audit log to one kind, or to every kind when [kind] is empty.
  void filterAudit(String kind) {
    if (kind == _auditKind) return;
    _auditKind = kind;
    replaceItems(_rows());
  }

  /// Prunes the unreferenced mirrors and orphaned work directories, and reads
  /// what is left.
  Future<PruneResult> prune() async {
    final client = _client;
    if (client == null) {
      return PruneResult(reclaimed: 0, total: _disk?.total ?? 0);
    }
    final result = await client.prune();
    _pruned = result;
    await load();
    return result;
  }

  /// Bytes as the design system writes them: MB to one decimal, or GB.
  static String bytes(int count) {
    const mb = 1024 * 1024;
    const gb = 1024 * mb;
    if (count >= gb) return '${(count / gb).toStringAsFixed(1)} GB';
    return '${(count / mb).toStringAsFixed(1)} MB';
  }

  List<UsageRow> _rows() {
    final report = _report;
    final disk = _disk;
    final events = [
      for (final event in _events)
        if (_auditKind.isEmpty || event.event == _auditKind) event,
    ];
    return [
      UsageRow(
        kind: UsageRowKind.filter,
        id: 'filter',
        name: 'Filter',
        description: 'The window, the grouping and the audit kind',
        value:
            '${_range == '7d' ? '7 days' : '30 days'} · '
            'by $_by${_auditKind.isEmpty ? '' : ' · audit $_auditKind'}',
      ),
      if (report != null)
        UsageRow(
          kind: UsageRowKind.spend,
          id: 'spend',
          name: 'Spend',
          description: 'What the fleet spent in this window',
          value: '\$${report.totals.costUSD.toStringAsFixed(2)}',
          facts: [
            ('Cost', '\$${report.totals.costUSD.toStringAsFixed(2)}'),
            (
              'Tokens',
              '${report.totals.promptTokens} in · '
                  '${report.totals.completionTokens} out',
            ),
            ('Agent hours', report.totals.agentHours.toStringAsFixed(1)),
            ('Pull requests', '${report.totals.pRsShipped} shipped'),
          ],
        ),
      if (report != null)
        for (final point in report.series)
          UsageRow(
            kind: UsageRowKind.series,
            id: 'series/${point.key}',
            name: point.key,
            description: 'Grouped by $_by',
            value:
                '\$${point.costUSD.toStringAsFixed(2)} · '
                '${_tokens(point.tokens)}',
            facts: [
              ('Cost', '\$${point.costUSD.toStringAsFixed(2)}'),
              ('Tokens', _tokens(point.tokens)),
            ],
          ),
      for (final event in events)
        UsageRow(
          kind: UsageRowKind.audit,
          id: 'audit/${event.ts}/${event.event}/${event.detail ?? ''}',
          name: event.event,
          description: [
            if (event.agentID != null && event.agentID!.isNotEmpty)
              event.agentID!,
            if (event.detail != null && event.detail!.isNotEmpty) event.detail!,
            if (event.reason != null && event.reason!.isNotEmpty) event.reason!,
          ].join(' · '),
          value: relativeAge(_time(event.ts), _now()),
          facts: [
            ('When', event.ts),
            if (event.agentID != null) ('Agent', event.agentID!),
            if (event.detail != null) ('Detail', event.detail!),
            if (event.bytes != null) ('Bytes', bytes(event.bytes!)),
          ],
        ),
      if (disk != null)
        UsageRow(
          kind: UsageRowKind.disk,
          id: 'disk',
          name: 'Disk',
          description:
              '${bytes(disk.repos)} of mirrors · ${bytes(disk.work)} of work',
          value: '${bytes(disk.total)} of ${disk.budgetMB} MB',
          facts: [
            ('Mirrors', bytes(disk.repos)),
            ('Work directories', bytes(disk.work)),
            ('Total', bytes(disk.total)),
            ('Budget', '${disk.budgetMB} MB'),
            ('Measured', disk.measuredAt),
            if (_pruned != null)
              (
                'Last prune',
                'freed ${bytes(_pruned!.reclaimed)}'
                    '${_pruned!.warning == null ? '' : ' · ${_pruned!.warning}'}',
              ),
          ],
        ),
    ];
  }

  static String _tokens(int count) =>
      count >= 1000 ? '${(count / 1000).round()}k tokens' : '$count tokens';

  static DateTime _time(String at) =>
      DateTime.tryParse(at) ??
      DateTime.fromMillisecondsSinceEpoch(0, isUtc: true);
}
