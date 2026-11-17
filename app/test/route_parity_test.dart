import 'dart:io';

import 'package:flutter_test/flutter_test.dart';

/// One row of `docs/studio-flutter/route-parity.md`: a route the Svelte client
/// calls, and the `BridgeClient` method that calls it — or why it does not.
class _Row {
  _Row(this.method, this.path, this.client, this.note);

  final String method;
  final String path;

  /// The `BridgeClient` method's name, or `—` when the route has no caller.
  final String client;
  final String note;

  bool get dropped => client == '—' || client.isEmpty;
}

/// F7's parity check: nothing the Svelte client calls is unmapped.
///
/// It reads the checked-in table and fails:
///
/// - when a row has neither a `BridgeClient` method nor a `dropped:` reason,
/// - when a row names a method that is not declared in `bridge_client.dart`,
/// - when the table stops naming the files it is a parity of, or loses rows.
///
/// The table is generated from those files and reviewed by hand: a route the
/// Dart client cannot reach says so, and why, rather than going quietly
/// missing.
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
      reason: 'the table lost rows: it is the parity of five client files',
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
}
