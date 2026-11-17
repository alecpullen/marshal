/// Reads the routes a `BridgeClient` source file can issue.
///
/// The client is a thin wrapper: every call ends in one of a handful of
/// helpers that take the path as a string literal. This walks those literals
/// the way Dart concatenates them — adjacent literals joined, `${…}` and `$x`
/// written `X` — and reads the HTTP method from the helper that encloses the
/// call: `_post` is POST, `_put` is PUT, `_listOf`, `_objectOf` and
/// `EventStream` are GET, and an explicit `method: '…'` wins over all of them.
///
/// A literal outside such a call is a path builder (the client's
/// `_secretPath`): the route it builds belongs to whichever caller hands the
/// path to a request helper, so it is skipped rather than guessed.
///
/// `route_parity_test.dart` uses this to check
/// `docs/studio-flutter/route-parity.md` in both directions.
library;

const _quote = 0x27;
const _backslash = 0x5C;
const _dollar = 0x24;
const _braceOpen = 0x7B;
const _braceClose = 0x7D;
const _parenOpen = 0x28;
const _parenClose = 0x29;

final _helperCall = RegExp(
  r'\b(_post|_put|_listOf|_objectOf|_send|_delete|_patch|EventStream'
  r'|_settingsOf|_draftOf)(?:<[^(\n]*>)?\(',
);

final _explicitMethod = RegExp(r"method:\s*'(\w+)'");
final _simpleInterpolation = RegExp(r'\$[A-Za-z_][A-Za-z0-9_]*');

/// The methods the helpers imply when the call names no other.
const _implied = <String, String>{
  '_post': 'POST',
  '_put': 'PUT',
  '_listOf': 'GET',
  'EventStream': 'GET',
};

/// Every route [source] can issue, as `METHOD /api/path`.
///
/// A templated segment is written `X` and any query string is dropped, which
/// is how `route-parity.md` writes a route.
Set<String> issuedRoutes(String source) {
  final routes = <String>{};
  for (final literal in RegExp(r"'/api/").allMatches(source)) {
    final start = literal.start;
    final helper = _enclosingHelper(source, start);
    if (helper == null) continue;
    final end = _callEnd(source, helper.end - 1);
    // A literal outside the call's parentheses is a path builder.
    if (start > end) continue;
    final path = _pathAt(source, start).split('?').first;
    routes.add('${_methodOf(source, helper, end)} $path');
  }
  return routes;
}

/// The last helper call that opens before [before].
RegExpMatch? _enclosingHelper(String source, int before) {
  RegExpMatch? last;
  for (final match in _helperCall.allMatches(source.substring(0, before))) {
    last = match;
  }
  return last;
}

String _methodOf(String source, RegExpMatch helper, int end) {
  final explicit = _explicitMethod.firstMatch(
    source.substring(helper.end, end),
  );
  if (explicit != null) return explicit.group(1)!;
  return _implied[helper.group(1)] ?? 'GET';
}

/// The index of the parenthesis closing the call that opens at [openParen].
int _callEnd(String source, int openParen) {
  var depth = 0;
  var inString = false;
  var i = openParen;
  while (i < source.length) {
    final unit = source.codeUnitAt(i);
    if (inString) {
      if (unit == _backslash) {
        i += 2;
        continue;
      }
      if (unit == _quote) inString = false;
    } else if (unit == _quote) {
      inString = true;
    } else if (unit == _parenOpen) {
      depth++;
    } else if (unit == _parenClose) {
      depth--;
      if (depth == 0) return i;
    }
    i++;
  }
  return source.length;
}

/// The path written at [quote], with adjacent literals joined.
String _pathAt(String source, int quote) {
  final buffer = StringBuffer();
  var i = quote;
  while (i < source.length && source.codeUnitAt(i) == _quote) {
    i++;
    while (i < source.length && source.codeUnitAt(i) != _quote) {
      final unit = source.codeUnitAt(i);
      if (unit == _backslash) {
        buffer.write(source.substring(i, i + 2));
        i += 2;
        continue;
      }
      if (unit == _dollar) {
        if (i + 1 < source.length && source.codeUnitAt(i + 1) == _braceOpen) {
          i = _skipInterpolation(source, i);
        } else {
          final name = _simpleInterpolation.matchAsPrefix(source, i);
          i = name?.end ?? i + 1;
        }
        buffer.write('X');
        continue;
      }
      buffer.writeCharCode(unit);
      i++;
    }
    i++; // the closing quote
    var next = i;
    while (next < source.length && _isSpace(source.codeUnitAt(next))) {
      next++;
    }
    // `'…/a/' '…/b'` is one path.
    if (next < source.length && source.codeUnitAt(next) == _quote) {
      i = next;
      continue;
    }
    break;
  }
  return buffer.toString();
}

/// The index just past the `}` closing the `${` at [dollar].
int _skipInterpolation(String source, int dollar) {
  var depth = 1;
  var inString = false;
  var i = dollar + 2;
  while (i < source.length) {
    final unit = source.codeUnitAt(i);
    if (inString) {
      if (unit == _backslash) {
        i += 2;
        continue;
      }
      if (unit == _quote) inString = false;
    } else if (unit == _quote) {
      inString = true;
    } else if (unit == _braceOpen) {
      depth++;
    } else if (unit == _braceClose) {
      depth--;
      if (depth == 0) return i + 1;
    }
    i++;
  }
  return source.length;
}

bool _isSpace(int unit) =>
    unit == 0x20 || unit == 0x09 || unit == 0x0A || unit == 0x0D;
