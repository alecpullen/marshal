import 'dart:io';

import 'package:flutter_test/flutter_test.dart';

import 'support/route_source.dart';

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

/// Route parity, one direction — the Studio against the bridge.
///
/// Until F8's cutover the table was a parity of two clients and this test read
/// the Svelte sources too. That client is gone, so the table now pairs the
/// bridge's routes with the one client there is, and the test reads the
/// checked-in table and fails:
///
/// - when a row has neither a `BridgeClient` method nor a `dropped:` reason,
/// - when a row names a method that is not declared in `bridge_client.dart`,
/// - when a row marked `dropped:` is a route the client issues,
/// - when the client issues a route no row lists,
/// - when the table loses rows.
///
/// The first two keep the table from describing a method that is gone. The
/// next two are the pair that matters: a route drifts from `dropped:` to
/// called when a later phase lands and the table is not revisited, and a new
/// call can land with no row at all. Both fail rather than going quiet.
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

  test('the parity table exists and keeps its rows', () {
    expect(table.existsSync(), isTrue, reason: 'missing ${table.path}');
    expect(
      rows(table.readAsStringSync()).length,
      greaterThanOrEqualTo(100),
      reason: 'the table lost rows: every route the bridge serves has one',
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
}
