import 'package:flutter/foundation.dart';

import '../client/contract.g.dart';

/// Whether the project states its own expected checks, or states nothing and
/// follows the bridge's behaviour for the work.
enum ExpectedChecksChoice { inherit, custom }

/// One check a factory work item must pass, as the forge names it: its kind,
/// its name and, for a `check_run`, the app that runs it.
///
/// This is the typed view of the wire's [FactoryCheckKey], so a check survives
/// a read and a write exactly: the kind and the name are kept verbatim, and
/// the app id stays the number the forge gave it rather than being collapsed
/// into the name.
@immutable
class ExpectedCheck {
  /// A check the forge runs as an app check. The wire's vocabulary only.
  static const checkRunKind = 'check_run';

  /// A check the forge reports as a commit status. The wire's vocabulary
  /// only.
  static const commitStatusKind = 'commit_status';

  const ExpectedCheck({required this.kind, required this.name, this.appID = 0});

  /// Reads a wire key without loss: its kind, its name and its app id.
  factory ExpectedCheck.of(FactoryCheckKey key) =>
      ExpectedCheck(kind: key.kind, name: key.name, appID: key.appID);

  /// Parses one canonical raw line. Throws [FormatException] when the line
  /// does not parse, or when it states no check at all.
  factory ExpectedCheck.parse(String line) {
    final parsed = parseExpectedChecks(line);
    final error = parsed.error;
    if (error != null) throw FormatException(error);
    if (parsed.checks.isEmpty) {
      throw const FormatException('No expected check is stated.');
    }
    return parsed.checks.single;
  }

  /// `check_run` or `commit_status`: the wire's vocabulary only.
  final String kind;

  /// The check's name, or a `commit_status`'s context. Kept verbatim, so its
  /// case and its colons are the forge's.
  final String name;

  /// The app that runs a `check_run`. Zero for a `commit_status`.
  final int appID;

  /// The check as the wire holds it, exactly.
  FactoryCheckKey toKey() =>
      FactoryCheckKey(kind: kind, name: name, appID: appID);

  /// The canonical raw line: `check_run:<name>:<app id>` or
  /// `commit_status:<context>`. A name or context may contain colons, so a
  /// `check_run`'s app id sits behind its line's last colon.
  String get line =>
      kind == commitStatusKind ? '$kind:$name' : '$kind:$name:$appID';

  /// The check in words a person reads: `build (App 42)` or `ci/lint`.
  String get words => kind == commitStatusKind ? name : '$name (App $appID)';

  @override
  bool operator ==(Object other) =>
      other is ExpectedCheck &&
      other.kind == kind &&
      other.name == name &&
      other.appID == appID;

  @override
  int get hashCode => Object.hash(kind, name, appID);
}

/// A check that already-recorded work in this project observed, and where it
/// saw it: the evidence an editor may offer, never a forge lookup.
///
/// It carries its own provenance so a suggestion says where it came from — the
/// work item's round and the moment — and a person can tell one recording from
/// another. It is a record of what happened, not a statement of what the forge
/// requires.
@immutable
class ObservedCheck {
  const ObservedCheck({
    required this.check,
    required this.observedAt,
    required this.revision,
  });

  /// The check as the forge named it, kept verbatim.
  final ExpectedCheck check;

  /// When the round observed it: the bridge's RFC3339 string, verbatim, so it
  /// is shown as the bridge wrote it and sorts as a date.
  final String observedAt;

  /// The round of the item that observed it.
  final int revision;

  /// Where the evidence came from, in words.
  String get provenance => 'revision $revision, observed $observedAt';
}

/// What parsing raw check text produced: the checks that parsed, the text as
/// the caller wrote it, and why the rest cannot be saved.
///
/// [checks] holds every check that parsed before the first bad line, so an
/// editor keeps what it can. [error] is not a refusal of [checks]: it says
/// the text cannot be stored as it is, and a caller that saves must treat it
/// as "do not save".
@immutable
class ExpectedChecksParse {
  const ExpectedChecksParse({
    required this.checks,
    required this.text,
    this.error,
  });

  final List<ExpectedCheck> checks;

  /// The caller's text, verbatim.
  final String text;

  /// Why the text cannot be saved, or null when it can.
  final String? error;
}

/// Parses the canonical raw lines, one check per line:
/// `check_run:<name>:<app id>` or `commit_status:<context>`.
///
/// The first colon names the kind. A `commit_status`'s context is the entire
/// remainder of its line, so a context may contain colons; a `check_run`'s
/// name is everything between the first and the last colon of what follows,
/// and the app id behind that last colon must be a positive number, so a name
/// may contain colons too. Blank lines are skipped, and text that states no
/// check at all is an empty list, not an error.
///
/// A line that is neither form stops the parse: [ExpectedChecksParse.checks]
/// holds what parsed so far, and [ExpectedChecksParse.error] names the line
/// and the rule. Nothing is dropped silently.
ExpectedChecksParse parseExpectedChecks(String text) {
  final checks = <ExpectedCheck>[];
  for (final raw in text.split('\n')) {
    final line = raw.trim();
    if (line.isEmpty) continue;
    final kindEnd = line.indexOf(':');
    if (kindEnd < 0) return _invalid(text, checks, line);
    final kind = line.substring(0, kindEnd);
    final rest = line.substring(kindEnd + 1);
    if (kind == ExpectedCheck.commitStatusKind) {
      // The context is the entire remainder of the line: it may contain
      // colons of its own.
      if (rest.isEmpty) return _invalid(text, checks, line);
      checks.add(ExpectedCheck(kind: kind, name: rest));
      continue;
    }
    if (kind == ExpectedCheck.checkRunKind) {
      // The last colon separates the app id from a name that may contain
      // colons of its own.
      final idStart = rest.lastIndexOf(':');
      if (idStart < 0) return _invalid(text, checks, line);
      final name = rest.substring(0, idStart);
      final appID = int.tryParse(rest.substring(idStart + 1));
      if (name.isEmpty || appID == null || appID <= 0) {
        return _invalid(text, checks, line);
      }
      checks.add(ExpectedCheck(kind: kind, name: name, appID: appID));
      continue;
    }
    return _invalid(text, checks, line);
  }
  return ExpectedChecksParse(checks: checks, text: text);
}

/// Stops the parse at [line]: the rule, and the line that broke it.
ExpectedChecksParse _invalid(
  String text,
  List<ExpectedCheck> checks,
  String line,
) => ExpectedChecksParse(
  checks: checks,
  text: text,
  error:
      'Each check must be check_run:<name>:<app id> or '
      'commit_status:<context>; "$line" is neither.',
);

/// Writes the canonical raw lines, one per check.
///
/// It is the exact inverse of [parseExpectedChecks] for any list whose names
/// and contexts hold no newline.
String formatExpectedChecks(List<ExpectedCheck> checks) =>
    [for (final check in checks) check.line].join('\n');
