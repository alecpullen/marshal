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
/// `svelteRoutePaths` reads the other client. It takes every `/api/` literal
/// in a Svelte source file — builders included, since a builder's route is a
/// route — and normalises it the way the table writes one. Comments are
/// stripped first: `api.ts` names routes in its doc comments, and a route in
/// prose is not a route a client calls.
///
/// `route_parity_test.dart` uses this to check
/// `docs/studio-flutter/route-parity.md` in every direction.
library;

const _quote = 0x27;
const _doubleQuote = 0x22;
const _backtick = 0x60;
const _backslash = 0x5C;
const _dollar = 0x24;
const _braceOpen = 0x7B;
const _braceClose = 0x7D;
const _parenOpen = 0x28;
const _parenClose = 0x29;
const _slash = 0x2F;
const _star = 0x2A;
const _newline = 0x0A;

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

// ---------------------------------------------------------------------------
// The Svelte client
// ---------------------------------------------------------------------------

/// A path literal starts where one of these quotes is followed by `/api/`.
final _svelteLiteral = RegExp(r'''['"`]/api/''');

final _bareIdentifier = RegExp(r'^[A-Za-z_$][A-Za-z0-9_$]*$');

/// Expressions that build a query string, not a path segment.
const _queryBuilders = ['query(', 'netQuery('];

/// Every route path a Svelte source names, in the form the table writes one.
///
/// The literal is read the way JavaScript joins it: a `${…}` or `$name` is a
/// path segment — `X` — unless it is the literal's tail and builds a query
/// string. The suffixes in `api.ts` are one of three shapes: a local
/// `'?path=…'` string, a ternary picking `?…` or `''`, or a `query(…)` /
/// `netQuery(…)` call. Each is dropped; everything else is a segment.
///
/// Comments are stripped first. `api.ts` writes `GET /api/budgets` in its doc
/// comments, and a route named in prose is not a route a client calls.
Set<String> svelteRoutePaths(String source) {
  final clean = stripJsComments(source);
  final queryLocals = _queryLocals(clean);
  final paths = <String>{};
  for (final match in _svelteLiteral.allMatches(clean)) {
    paths.add(_sveltePathAt(clean, match.start, queryLocals).split('?').first);
  }
  return paths;
}

/// Names a file declares as a query-string builder.
///
/// `const q = path ? \`?path=…\` : ''` makes `${q}` a query, while `${port}`
/// is a parameter and stays a path segment. Only the former is a query local:
/// the distinction is the declaration, not the name.
Set<String> _queryLocals(String source) {
  final names = <String>{};
  for (final match in RegExp(
    r'\b(?:const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=\s*([^\n;]+)',
  ).allMatches(source)) {
    final value = match.group(2)!.trim();
    // A string that opens with `?` is a query prefix.
    if (_queryPrefix.hasMatch(value) || value.startsWith('query(')) {
      names.add(match.group(1)!);
    }
  }
  return names;
}

final _queryPrefix = RegExp(r'''[`'"]\?''');

/// The path written by the literal whose opening quote is at [quoteIndex].
String _sveltePathAt(String source, int quoteIndex, Set<String> queryLocals) {
  final quote = source.codeUnitAt(quoteIndex);
  final buffer = StringBuffer();
  var i = quoteIndex + 1;
  while (i < source.length) {
    final unit = source.codeUnitAt(i);
    if (unit == _backslash) {
      buffer.write(source.substring(i, i + 2));
      i += 2;
      continue;
    }
    if (unit == quote) break;
    if (unit == _dollar) {
      if (i + 1 < source.length && source.codeUnitAt(i + 1) == _braceOpen) {
        final end = _skipInterpolation(source, i);
        final expression = source.substring(i + 2, end - 1);
        if (!_isQuerySuffix(source, end, quote, expression, queryLocals)) {
          buffer.write('X');
        }
        i = end;
        continue;
      }
      final name = _simpleInterpolation.matchAsPrefix(source, i);
      if (name != null) {
        final query =
            _isLiteralTail(source, name.end, quote) &&
            queryLocals.contains(name.group(0)!.substring(1));
        if (!query) buffer.write('X');
        i = name.end;
        continue;
      }
    }
    buffer.writeCharCode(unit);
    i++;
  }
  return buffer.toString();
}

/// Whether the interpolation ending at [end] builds a query string.
bool _isQuerySuffix(
  String source,
  int end,
  int quote,
  String expression,
  Set<String> queryLocals,
) {
  if (!_isLiteralTail(source, end, quote)) return false;
  final text = expression.trim();
  // A ternary picks between `?…` and `''`: a query either way.
  if (text.contains('?')) return true;
  // A bare name is a query only where the file declares it as one.
  if (_bareIdentifier.hasMatch(text)) return queryLocals.contains(text);
  return _queryBuilders.any(text.startsWith);
}

/// Whether the literal closes at [from], whitespace aside.
bool _isLiteralTail(String source, int from, int quote) {
  var i = from;
  while (i < source.length && _isSpace(source.codeUnitAt(i))) {
    i++;
  }
  return i < source.length && source.codeUnitAt(i) == quote;
}

/// [source] with `//` and `/* */` comments removed.
///
/// A template literal nests: ``` `${a ? `?b=${c}` : ''}` ``` is a string, then
/// code, then a string again. The walk keeps a stack so a `//` inside a
/// nested string is not taken for a comment. A comment is replaced by a single
/// space so the tokens either side do not fuse.
String stripJsComments(String source) {
  final out = StringBuffer();
  final stack = <_JsFrame>[_JsFrame.code()];
  var i = 0;
  while (i < source.length) {
    final top = stack.last;
    final unit = source.codeUnitAt(i);
    if (!top.isCode) {
      if (unit == _backslash) {
        out.write(source.substring(i, i + 2 <= source.length ? i + 2 : i + 1));
        i += 2;
        continue;
      }
      if (unit == top.quote) {
        stack.removeLast();
        out.writeCharCode(unit);
        i++;
        continue;
      }
      if (top.quote == _backtick &&
          unit == _dollar &&
          i + 1 < source.length &&
          source.codeUnitAt(i + 1) == _braceOpen) {
        out.write(r'${');
        stack.add(_JsFrame.code());
        i += 2;
        continue;
      }
      out.writeCharCode(unit);
      i++;
      continue;
    }
    if (unit == _slash && i + 1 < source.length) {
      final next = source.codeUnitAt(i + 1);
      if (next == _slash) {
        final end = source.indexOf('\n', i);
        out.writeCharCode(_newline);
        i = end < 0 ? source.length : end + 1;
        continue;
      }
      if (next == _star) {
        final end = source.indexOf('*/', i + 2);
        out.write(' ');
        i = end < 0 ? source.length : end + 2;
        continue;
      }
    }
    if (unit == _quote || unit == _doubleQuote || unit == _backtick) {
      stack.add(_JsFrame.string(unit));
    } else if (unit == _braceOpen) {
      top.depth++;
    } else if (unit == _braceClose && top.depth > 0) {
      top.depth--;
    } else if (unit == _braceClose && stack.length > 1) {
      // The `}` that closes a `${`: back into the template it belongs to.
      stack.removeLast();
    }
    out.writeCharCode(unit);
    i++;
  }
  return out.toString();
}

class _JsFrame {
  _JsFrame.code() : isCode = true, quote = 0, depth = 0;

  _JsFrame.string(this.quote) : isCode = false, depth = 0;

  final bool isCode;
  final int quote;
  int depth;
}
