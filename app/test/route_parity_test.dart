import 'dart:io';

import 'package:flutter_test/flutter_test.dart';

import 'support/route_source.dart';

/// The Svelte client's route sources, relative to `app/`.
const _svelteSources = [
  'api.ts',
  'factory.ts',
  'planner.ts',
  'working-drafts.ts',
  'sse.ts',
];

/// One row of `docs/studio-flutter/route-parity.md`: a route a client calls,
/// and the `BridgeClient` method that calls it — or why it does not.
class _Row {
  _Row(this.method, this.path, this.client, this.note);

  final String method;
  final String path;

  /// The `BridgeClient` method's name, or `—` when the route has no caller.
  final String client;
  final String note;

  bool get dropped => client == '—' || client.isEmpty;

  /// The route as the client writes it: the method and the path, without the
  /// query string the table spells out for a reader.
  String get route => '$method ${path.split('?').first}';
}

/// Route parity, in both directions.
///
/// It reads the checked-in table and fails:
///
/// - when a row has neither a `BridgeClient` method nor a `dropped:` reason,
/// - when a row names a method that is not declared in `bridge_client.dart`,
/// - when a row marked `dropped:` is a route the client issues,
/// - when the client issues a route no row lists,
/// - when the table stops naming the files it is a parity of, or loses rows.
///
/// The first two keep the table from describing a method that is gone. The
/// last two are the pair that matters: a route drifts from `dropped:` to
/// called when a later phase lands and the table is not revisited, and a new
/// call can land with no row at all. Both now fail rather than going quiet.
void main() {
  final table = File('../docs/studio-flutter/route-parity.md');
  final client = File('lib/src/client/bridge_client.dart');

  List<_Row> rows(String text) {
    final out = <_Row>[];
    for (final line in text.split('\n')) {
      if (!line.startsWith('| ')) continue;
      final cells = line.split('|').map((cell) => cell.trim()).toList();
      // A row is `| METHOD | path | client | note |`: six cells, the ends empty.
      if (cells.length < 6) continue;
      final method = cells[1];
      if (method == 'Method' || method.startsWith('---')) continue;
      out.add(
        _Row(
          method,
          cells[2].replaceAll('`', ''),
          cells[3],
          cells.sublist(4, cells.length - 1).join(' | '),
        ),
      );
    }
    return out;
  }

  test('the parity table exists and covers every source file', () {
    expect(table.existsSync(), isTrue, reason: 'missing ${table.path}');
    final text = table.readAsStringSync();
    for (final source in [
      'web/ui/src/lib/api.ts',
      'factory.ts',
      'planner.ts',
      'working-drafts.ts',
      'sse.ts',
    ]) {
      expect(
        text.contains(source),
        isTrue,
        reason: 'the table does not name $source among its sources',
      );
    }
    expect(
      rows(text).length,
      greaterThanOrEqualTo(100),
      reason: 'the table lost rows: it is the parity of two clients',
    );
  });

  test('every route is mapped or dropped', () {
    final parsed = rows(table.readAsStringSync());
    expect(parsed, isNotEmpty);
    final problems = <String>[];
    for (final row in parsed) {
      if (row.dropped) {
        if (!row.note.startsWith('dropped:')) {
          problems.add(
            '${row.method} ${row.path}: no BridgeClient method and no '
            'dropped: reason',
          );
        }
        continue;
      }
      final declaration = RegExp(
        r'^\s*(?:Future<[^\n]*?>|EventStream)\s+'
        '${RegExp.escape(row.client)}\\(',
        multiLine: true,
      );
      if (!declaration.hasMatch(client.readAsStringSync())) {
        problems.add(
          '${row.method} ${row.path}: ${row.client} is not a BridgeClient '
          'method',
        );
      }
    }
    expect(problems, isEmpty, reason: problems.join('\n'));
  });

  test('a dropped row says why, in one sentence', () {
    for (final row in rows(table.readAsStringSync())) {
      if (!row.dropped) continue;
      expect(
        row.note.length,
        greaterThan('dropped: '.length + 10),
        reason: '${row.method} ${row.path} says only "${row.note}"',
      );
    }
  });

  test('a dropped route is not one the client issues', () {
    final issued = issuedRoutes(client.readAsStringSync());
    final wrong = [
      for (final row in rows(table.readAsStringSync()))
        if (row.dropped && issued.contains(row.route)) row.route,
    ]..sort();
    expect(
      wrong,
      isEmpty,
      reason:
          'these routes are marked dropped and the client issues them; '
          'map each to its BridgeClient method:\n${wrong.join('\n')}',
    );
  });

  test('the table lists every route the client issues', () {
    final listed = {
      for (final row in rows(table.readAsStringSync())) row.route,
    };
    final missing = [
      for (final route in issuedRoutes(client.readAsStringSync()))
        if (!listed.contains(route)) route,
    ]..sort();
    expect(
      missing,
      isEmpty,
      reason:
          'the client issues these routes and no row lists one; add them '
          'to the table:\n${missing.join('\n')}',
    );
  });

  test('the table lists every route the Svelte client calls', () {
    final listed = {
      for (final row in rows(table.readAsStringSync()))
        row.path.split('?').first,
    };
    final missing = <String>[];
    for (final name in _svelteSources) {
      final source = File('../web/ui/src/lib/$name');
      expect(
        source.existsSync(),
        isTrue,
        reason:
            'missing ${source.path}: the table is a parity of this client, so '
            'a renamed source silently stops being checked',
      );
      for (final path in svelteRoutePaths(source.readAsStringSync())) {
        if (!listed.contains(path)) missing.add('$path  ($name)');
      }
    }
    missing.sort();
    expect(
      missing,
      isEmpty,
      reason:
          'the Svelte client calls these routes and no row lists one; add '
          'them to the table:\n${missing.join('\n')}',
    );
  });
}
