import 'package:flutter_test/flutter_test.dart';

import 'support/source.dart';

/// design-system.md §11, the rules read from source. Every file under
/// `lib/src/ui/` is read with its comments and string literals blanked, so a
/// doc comment or a tooltip that mentions `Colors.red` is not a colour; then
/// each rule looks for what it forbids. The rules are deliberately blunt: a
/// failure is answered by using a token or extending the system (§10), not
/// by arguing with the test.
void main() {
  const ui = 'lib/src/ui';
  const tokens = '$ui/tokens.dart';
  final files = dartFilesUnder(ui);
  final styled = [
    for (final f in files)
      if (f.path != tokens) f,
  ];

  /// `path:line: text` for each match of [pattern] in [file]'s code, read
  /// with [accept] deciding whether a match is allowed.
  List<String> matches(
    SourceFile file,
    RegExp pattern, {
    bool Function(RegExpMatch match)? accept,
  }) => [
    for (final m in pattern.allMatches(file.code))
      if (accept == null || !accept(m))
        '${file.path}:${file.lineOf(m.start)}: ${m.group(0)!.trim()}',
  ];

  void expectNone(List<String> found, String rule) {
    expect(found, isEmpty, reason: '$rule\n${found.join('\n')}');
  }

  test('colours come from tokens', () {
    final forbidden = [
      // A colour constructed or derived anywhere but tokens.dart.
      RegExp(r'\bColor\s*\('),
      RegExp(r'\bColor\.(fromARGB|fromRGBO|from|lerp|alphaBlend)\b'),
      RegExp(r'\b(HSLColor|HSVColor)\b'),
      RegExp(
        r'\.(withValues|withOpacity|withAlpha|withRed|withGreen|withBlue)\s*\(',
      ),
      // Named Material and Cupertino colours.
      RegExp(r'\b(Colors|CupertinoColors)\s*\.'),
      // A colour written as a number.
      RegExp(r'\b0x[0-9A-Fa-f]{6,8}\b'),
    ];
    expectNone([
      for (final f in styled)
        for (final p in forbidden) ...matches(f, p),
    ], 'a colour outside tokens.dart; use a token from Ember');
  });

  test('text styles come from tokens', () {
    final forbidden = [
      RegExp(r'\bTextStyle\s*\('),
      RegExp(r'\bTextStyle\.lerp\b'),
      RegExp(r'\b(TextTheme|Typography)\s*\('),
      RegExp(
        r'\b(fontSize|fontWeight|fontFamily|fontFamilyFallback|letterSpacing|fontSizeFactor|fontSizeDelta)\s*:',
      ),
      RegExp(r'\bFontWeight\s*\.'),
      RegExp(r'\bGoogleFonts\b'),
    ];
    expectNone([
      for (final f in styled)
        for (final p in forbidden) ...matches(f, p),
    ], 'a text style outside tokens.dart; use a role from Ember');
  });

  // §4's scales: spacing, the sizes (border, icons, controls, touch) and
  // the layout widths. Zero is no space at all.
  const onScale = {
    0, 2, 4, 6, 8, 12, 16, 20, 24, 32, // spacing
    1, // border
    11, 13, 15, // icons
    28, 30, 44, // controls, touch target
    252, 400, 760, 860, 1080, 1180, // layout
  };
  final number = RegExp(r'-?\b\d+(?:\.\d+)?\b|-?\.\d+\b');
  bool numberOnScale(String n) {
    final v = double.parse(n.startsWith('-') ? n.substring(1) : n);
    return onScale.any((s) => s == v);
  }

  test('spacing is on the scale', () {
    // A literal given as a padding, a gap or a box size. (`start` and `end`
    // are left to the EdgeInsetsDirectional scan below: a Tween has an end
    // that is not a length.)
    final named = RegExp(
      r'\b(width|height|minWidth|maxWidth|minHeight|maxHeight|left|top|right|'
      r'bottom|horizontal|vertical|spacing|runSpacing|'
      r'mainAxisSpacing|crossAxisSpacing|indent|endIndent|thickness|'
      r'dimension|size|itemExtent|extent|gap)\s*:\s*(-?(?:\d+(?:\.\d+)?|\.\d+))\b',
    );
    // Every number inside an EdgeInsets, a Size or a SizedBox shape call.
    final call = RegExp(
      r'\b(EdgeInsets(?:Directional)?\.\w+|Size(?:\.\w+)?|'
      r'SizedBox\.(?:square|fromSize))\s*\(',
    );
    // A constant declared with a bare number, which a box then uses.
    final constant = RegExp(
      r'\b(?:const|final)\s+(?:double\s+|int\s+)?\w+\s*=\s*(-?\d+(?:\.\d+)?)\s*;',
    );

    final found = <String>[];
    for (final f in styled) {
      found.addAll(
        matches(f, named, accept: (m) => numberOnScale(m.group(2)!)),
      );
      for (final m in call.allMatches(f.code)) {
        final args = _argumentsAt(f.code, m.end - 1);
        for (final n in number.allMatches(args)) {
          if (!numberOnScale(n.group(0)!)) {
            found.add(
              '${f.path}:${f.lineOf(m.start)}: ${m.group(1)}(…${n.group(0)}…)',
            );
          }
        }
      }
      found.addAll(
        matches(f, constant, accept: (m) => numberOnScale(m.group(1)!)),
      );
    }
    expectNone(found, 'a number off the §4 scales; use Space, Sizes or Layout');
  });

  test('radii are on the scale', () {
    const radii = {12, 8, 2}; // panel, control, bar; full is Radii.full
    final circular = RegExp(
      r'\b(?:BorderRadius(?:Directional)?|Radius)\.circular\s*\(\s*'
      r'(-?(?:\d+(?:\.\d+)?|\.\d+))\b',
    );
    final named = RegExp(r'\bradius\s*:\s*(-?(?:\d+(?:\.\d+)?|\.\d+))\b');
    final other = RegExp(
      r'\b(Radius\.elliptical|Radius\.zero|BorderRadius\.zero)\b',
    );
    bool ok(RegExpMatch m) => radii.contains(double.parse(m.group(1)!));
    expectNone([
      for (final f in styled) ...[
        ...matches(f, circular, accept: ok),
        ...matches(f, named, accept: ok),
        ...matches(f, other),
      ],
    ], 'a radius that is not 12, 8, 2 or full; use Radii');
  });

  test('screens use the kit', () {
    bool materialOrCupertino(String uri) =>
        uri.startsWith('package:material_ui/') ||
        uri.startsWith('package:cupertino_ui/') ||
        uri.startsWith('package:cupertino_icons/') ||
        uri == 'package:flutter/material.dart' ||
        uri == 'package:flutter/cupertino.dart';
    expectNone([
      for (final f in files)
        if (!f.isUnder('$ui/kit') && f.path != tokens)
          for (final uri in f.directiveUris)
            if (materialOrCupertino(uri)) '${f.path}: $uri',
    ], 'a screen imports Material or Cupertino; build it from the kit');
  });

  test('kit components do not fetch', () {
    expectNone([
      for (final f in files)
        if (f.isUnder('$ui/kit'))
          for (final uri in f.directiveUris)
            if (_isLayer(f.resolve(uri), 'lib/src/model') ||
                _isLayer(f.resolve(uri), 'lib/src/client'))
              '${f.path}: $uri',
    ], 'a kit component reaches for model/ or client/; pass it data');
  });

  test('terminal colours stay in the terminal widget', () {
    // §2 "Terminal colours": termBlue, termMagenta and termCyan exist for
    // the terminal's ANSI palette and for nothing else. The terminal widget
    // is `terminal_view.dart` (F4); tokens.dart defines them.
    final terminalOnly = RegExp(r'\b(termBlue|termMagenta|termCyan)\b');
    expectNone([
      for (final f in files)
        if (f.path != tokens && f.name != 'terminal_view.dart')
          ...matches(f, terminalOnly),
    ], 'a terminal-only colour used outside terminal_view.dart');
  });

  test('the rules read real code', () {
    // A guard against the rules going quiet: the kit is where the tokens
    // are used, so the scan must see it, and must see tokens.dart's own
    // colours when it is not exempted.
    expect(files.where((f) => f.isUnder('$ui/kit')), isNotEmpty);
    final palette = files.singleWhere((f) => f.path == tokens);
    expect(RegExp(r'\bColor\s*\(').hasMatch(palette.code), isTrue);
  });
}

bool _isLayer(String? path, String layer) =>
    path != null && (path == layer || path.startsWith('$layer/'));

/// The text between the parenthesis at [open] and its match.
String _argumentsAt(String code, int open) {
  var depth = 0;
  for (var i = open; i < code.length; i++) {
    if (code[i] == '(') depth++;
    if (code[i] == ')') {
      depth--;
      if (depth == 0) return code.substring(open + 1, i);
    }
  }
  return code.substring(open + 1);
}
