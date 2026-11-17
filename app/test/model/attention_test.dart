import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/attention.dart';
import 'package:marshal_app/src/model/inbox_model.dart';

/// Where an attention entry says it came from (Task 1 of the attention plan):
/// its source, its project, its scope, and the two rules the screens used to
/// guess — blocking and thread-only.
void main() {
  final since = DateTime.utc(2026, 11, 17, 12);

  Ask ask(AskKind kind) => Ask(
    host: 'local',
    id: 'ask-1',
    kind: kind,
    thread: null,
    title: 'git push',
    body: '',
    actions: const [],
    since: since,
    source: AskSource.fleet,
    scope: AskScope.everyone,
  );

  test('every source has a label and a note that says more than the label', () {
    for (final source in AskSource.values) {
      expect(source.label.trim(), isNotEmpty, reason: source.name);
      expect(source.note.trim(), isNotEmpty, reason: source.name);
      // A note that repeated the label would add nothing a tooltip can use.
      expect(source.note, isNot(source.label), reason: source.name);
    }
  });

  test('every scope has a label and a note', () {
    for (final scope in AskScope.values) {
      expect(scope.label.trim(), isNotEmpty, reason: scope.name);
      expect(scope.note.trim(), isNotEmpty, reason: scope.name);
      expect(scope.note, isNot(scope.label), reason: scope.name);
    }
  });

  test('provenance names the source, the project and the scope', () {
    expect(
      provenanceOf(AskSource.factory, '/home/u/marshal', AskScope.projectWide),
      'Factory · marshal · Project-wide',
    );
  });

  test('provenance omits a project the source does not name', () {
    // No project is not an empty field: it is a two-part line, with no
    // separator left dangling where the project would have been.
    final line = provenanceOf(AskSource.fleet, '', AskScope.everyone);
    expect(line, 'Fleet · Everyone');
    expect(line.split(' · '), ['Fleet', 'Everyone']);
    expect(line.contains('·  ·'), isFalse);
  });

  test('blocking is the source kind\u2019s own claim', () {
    const blocking = [
      AskKind.approval,
      AskKind.question,
      AskKind.approvalDraft,
      AskKind.decision,
      AskKind.recovery,
      AskKind.pull,
      AskKind.reviewDraft,
      AskKind.network,
    ];
    const notBlocking = [AskKind.intake, AskKind.completion];

    for (final kind in blocking) {
      expect(ask(kind).blocking, isTrue, reason: kind.name);
    }
    for (final kind in notBlocking) {
      expect(ask(kind).blocking, isFalse, reason: kind.name);
    }
    // Every kind is decided, so a new one cannot slip through by default.
    expect({...blocking, ...notBlocking}, AskKind.values.toSet());
  });

  test('answeredInThread is the four factory kinds and nothing else', () {
    const threadOnly = [
      AskKind.approvalDraft,
      AskKind.decision,
      AskKind.recovery,
      AskKind.completion,
    ];
    final rest = [
      for (final kind in AskKind.values)
        if (!threadOnly.contains(kind)) kind,
    ];

    for (final kind in threadOnly) {
      expect(ask(kind).answeredInThread, isTrue, reason: kind.name);
    }
    for (final kind in rest) {
      expect(ask(kind).answeredInThread, isFalse, reason: kind.name);
    }
    expect(rest, hasLength(AskKind.values.length - 4));
  });
}
