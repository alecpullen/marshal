import 'dart:io';

/// Reading the app's own Dart source, for the tests that enforce a rule by
/// looking at code rather than at a running widget: `boundary_test.dart` and
/// `design_rules_test.dart`.
///
/// The tests run from the app directory (`flutter test` sets it), so every
/// path here is relative to it and uses forward slashes: `lib/src/ui/kit/box.dart`.
class SourceFile {
  SourceFile(this.path, this.text);

  /// The path from the app directory, with forward slashes.
  final String path;

  /// The file as written.
  final String text;

  /// The file with its comments blanked out, strings kept. Directives are
  /// read from this, so a commented-out import is not an import.
  late final String withoutComments = strip(text, blankStrings: false);

  /// The file with comments and string literals blanked out. The design
  /// rules match against this, so a doc comment or a tooltip that mentions
  /// `Colors.red` is not a colour.
  late final String code = strip(text, blankStrings: true);

  /// Every URI named by an `import` or `export` directive, including each
  /// branch of a configurable import (`import 'a.dart' if (dart.library.io)
  /// 'b.dart';`).
  late final List<String> directiveUris = _directiveUris(withoutComments);

  /// The 1-based line number of [offset] in [text].
  int lineOf(int offset) =>
      '\n'.allMatches(text.substring(0, offset)).length + 1;

  /// Whether this file is [dir] or lies under it.
  bool isUnder(String dir) =>
      path.startsWith(dir.endsWith('/') ? dir : '$dir/');

  /// The name of the file without its directory.
  String get name => path.substring(path.lastIndexOf('/') + 1);

  /// Resolves a directive URI to a path from the app directory, when it
  /// names a file of this package; null for `dart:` libraries and other
  /// packages.
  String? resolve(String uri) {
    const self = 'package:marshal_app/';
    if (uri.startsWith(self)) return 'lib/${uri.substring(self.length)}';
    if (uri.contains(':')) return null;
    final dir = path.substring(0, path.lastIndexOf('/'));
    return normalize('$dir/$uri');
  }
}

/// Every `.dart` file under [root] (a directory relative to the app), sorted
/// by path. Throws if [root] is missing, so a moved directory fails the test
/// that relies on it instead of making it vacuous.
List<SourceFile> dartFilesUnder(String root) {
  final dir = Directory(root);
  if (!dir.existsSync()) {
    throw StateError(
      '$root was not found; run the tests from the app directory',
    );
  }
  final files = <SourceFile>[];
  for (final entity in dir.listSync(recursive: true)) {
    if (entity is! File || !entity.path.endsWith('.dart')) continue;
    final path = entity.path.replaceAll(r'\', '/');
    files.add(SourceFile(path, entity.readAsStringSync()));
  }
  files.sort((a, b) => a.path.compareTo(b.path));
  return files;
}

/// Collapses `.` and `..` segments in a forward-slash path.
String normalize(String path) {
  final out = <String>[];
  for (final segment in path.split('/')) {
    if (segment.isEmpty || segment == '.') continue;
    if (segment == '..' && out.isNotEmpty && out.last != '..') {
      out.removeLast();
    } else {
      out.add(segment);
    }
  }
  return out.join('/');
}

// The `;` before a directive is looked behind, not consumed: otherwise the
// previous directive's own `;` is spent and the next import is never read.
final _directive = RegExp(r'(?:^|(?<=[;}]))\s*(?:import|export)\b([^;]*);');
final _quoted = RegExp(r'''(?:'([^']*)'|"([^"]*)")''');

List<String> _directiveUris(String source) {
  final uris = <String>[];
  for (final match in _directive.allMatches(source)) {
    for (final quoted in _quoted.allMatches(match.group(1)!)) {
      uris.add(quoted.group(1) ?? quoted.group(2)!);
    }
  }
  return uris;
}

/// Replaces every comment in [source] with spaces, and with [blankStrings]
/// every string literal too, keeping newlines so offsets and line numbers
/// still match the original. Nested block comments, raw strings, triple
/// quotes and `${}` interpolations are understood; a string's interpolated
/// code is blanked with the string.
String strip(String source, {required bool blankStrings}) {
  final out = StringBuffer();
  var i = 0;

  void blank(int from, int to) {
    for (var k = from; k < to; k++) {
      out.write(source[k] == '\n' ? '\n' : ' ');
    }
  }

  while (i < source.length) {
    final end = _commentEnd(source, i);
    if (end != null) {
      blank(i, end);
      i = end;
      continue;
    }
    if (_startsString(source, i)) {
      final close = _stringEnd(source, i);
      if (blankStrings) {
        blank(i, close);
      } else {
        out.write(source.substring(i, close));
      }
      i = close;
      continue;
    }
    out.write(source[i]);
    i++;
  }
  return out.toString();
}

/// If a comment starts at [i], the index just past it; otherwise null.
int? _commentEnd(String s, int i) {
  if (i + 1 >= s.length || s[i] != '/') return null;
  if (s[i + 1] == '/') {
    final newline = s.indexOf('\n', i);
    return newline < 0 ? s.length : newline;
  }
  if (s[i + 1] != '*') return null;
  var depth = 0;
  var k = i;
  while (k < s.length) {
    if (s.startsWith('/*', k)) {
      depth++;
      k += 2;
    } else if (s.startsWith('*/', k)) {
      depth--;
      k += 2;
      if (depth == 0) return k;
    } else {
      k++;
    }
  }
  return s.length;
}

bool _isIdentifierChar(String c) => RegExp(r'[A-Za-z0-9_$]').hasMatch(c);

bool _startsString(String s, int i) {
  final c = s[i];
  if (c == "'" || c == '"') return true;
  if ((c == 'r' || c == 'R') &&
      i + 1 < s.length &&
      (s[i + 1] == "'" || s[i + 1] == '"')) {
    return i == 0 || !_isIdentifierChar(s[i - 1]);
  }
  return false;
}

/// The index just past the string literal that starts at [start].
int _stringEnd(String s, int start) {
  var k = start;
  final raw = s[k] == 'r' || s[k] == 'R';
  if (raw) k++;
  final quote = s[k];
  final triple = s.startsWith(quote * 3, k);
  final closing = triple ? quote * 3 : quote;
  k += closing.length;
  while (k < s.length) {
    final c = s[k];
    if (!raw && c == r'\') {
      k += 2;
      continue;
    }
    if (!raw && c == r'$' && k + 1 < s.length && s[k + 1] == '{') {
      k = _codeEnd(s, k + 2);
      continue;
    }
    if (s.startsWith(closing, k)) return k + closing.length;
    if (!triple && c == '\n') return k;
    k++;
  }
  return s.length;
}

/// The index just past the `}` that closes an interpolation whose code
/// starts at [start].
int _codeEnd(String s, int start) {
  var depth = 0;
  var k = start;
  while (k < s.length) {
    final comment = _commentEnd(s, k);
    if (comment != null) {
      k = comment;
      continue;
    }
    if (_startsString(s, k)) {
      k = _stringEnd(s, k);
      continue;
    }
    final c = s[k];
    if (c == '{') depth++;
    if (c == '}') {
      if (depth == 0) return k + 1;
      depth--;
    }
    k++;
  }
  return s.length;
}
