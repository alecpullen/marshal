import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/attention.dart' as model;
import 'package:marshal_app/src/model/inbox_model.dart' as model;
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/thread_ref.dart' as model;
import 'package:marshal_app/src/model/pulls_model.dart';
import 'package:marshal_app/src/model/source_state.dart';
import 'package:marshal_app/src/ui/inbox/inbox.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/pulls.dart';
import '../support/screens.dart';

/// The Inbox (F2 decision 7): every ask answered from its own card, a reply
/// that carries its words, a failure that keeps the card with a retry, and
/// the three filters.
void main() {
  final answers = <String>[];
  final replies = <String?>[];
  final collected = <List<String>?>[];

  model.Ask ask({
    required String id,
    required model.AskKind kind,
    required List<String> actions,
    List<model.AskAction>? askActions,
    List<model.AskQuestion> questions = const [],
    model.AskState state = model.AskState.waiting,
    String? error,
    String? title,
    bool withThread = true,
    Map<String, Object?>? params,
    model.AskSource source = model.AskSource.fleet,
    String project = '',
    model.AskScope scope = model.AskScope.everyone,
  }) => model.Ask(
    host: 'local',
    id: id,
    kind: kind,
    thread: withThread
        ? model.ThreadRef(
            host: 'local',
            id: 'a-1',
            project: '/home/u/marshal',
            title: 'Reliable session recovery',
            kind: model.ThreadKind.agent,
            status: model.ThreadStatus.needsYou,
            headline: '',
            lastActive: shellNow,
          )
        : null,
    title:
        title ??
        (kind == model.AskKind.question ? 'Which branch?' : 'git push'),
    body: '',
    actions:
        askActions ??
        [
          for (final (index, label) in actions.indexed)
            model.AskAction(
              label,
              primary: index == 0,
              kind: label == 'Reply'
                  ? model.AskActionKind.reply
                  : model.AskActionKind.option,
            ),
        ],
    questions: questions,
    since: shellNow.subtract(const Duration(minutes: 12)),
    source: source,
    project: project,
    scope: scope,
    state: state,
    error: error,
  );

  /// The answers posted, as "ask id:label": the list the widget calls back
  /// into, named once here so a closure's own `answers` can shadow it.
  final posted = answers;

  Widget show(
    List<model.Ask> asks, {
    StateKind? state,
    List<SourceState> sources = const [],
    List<PullView>? pulls,
    void Function(Place)? onSelect,
  }) => EmberApp(
    brightness: Brightness.light,
    home: Inbox(
      asks: asks,
      now: shellNow,
      state: state,
      sources: sources,
      pulls: pulls ?? const [],
      onSelect: onSelect ?? (_) {},
      onAnswer: (ask, action, {reply, answers}) {
        posted.add('${ask.id}:${action.label}');
        replies.add(reply);
        collected.add(answers);
      },
      onRetry: () {},
    ),
  );

  setUp(() {
    answers.clear();
    replies.clear();
    collected.clear();
  });

  testWidgets('approve once posts the decision', (tester) async {
    await tester.pumpWidget(
      show([
        ask(
          id: 'tc-1',
          kind: model.AskKind.approval,
          actions: ['Approve once', 'Deny'],
        ),
      ]),
    );

    await tester.tap(find.text('Approve once'));
    await tester.pump();

    expect(answers, ['tc-1:Approve once']);
    expect(replies, [null]);
  });

  testWidgets('deny posts a denial', (tester) async {
    await tester.pumpWidget(
      show([
        ask(
          id: 'tc-1',
          kind: model.AskKind.approval,
          actions: ['Approve once', 'Deny'],
        ),
      ]),
    );

    await tester.tap(find.text('Deny'));
    await tester.pump();

    expect(answers, ['tc-1:Deny']);
  });

  testWidgets('reply posts free text', (tester) async {
    await tester.pumpWidget(
      show([
        ask(
          id: 'q-1',
          kind: model.AskKind.question,
          actions: ['main', 'Reply'],
          params: {
            'questions': [
              {'question': 'Which branch?'},
            ],
          },
        ),
      ]),
    );

    await tester.tap(find.text('Reply'));
    await tester.pump();
    await tester.enterText(find.byType(Field), 'a topic branch');
    await tester.tap(find.text('Send'));
    await tester.pump();

    expect(answers, ['q-1:Reply']);
    expect(replies, ['a topic branch']);
  });

  testWidgets('a two-question card collects both answers', (tester) async {
    const questions = [
      model.AskQuestion(
        text: 'Which branch?',
        options: [
          model.AskOption('main'),
          model.AskOption('next', value: 'release/next'),
        ],
      ),
      model.AskQuestion(text: 'When should it land?'),
    ];
    await tester.pumpWidget(
      show([
        ask(
          id: 'q-2',
          kind: model.AskKind.question,
          actions: const [],
          askActions: const [
            model.AskAction(
              'Answer',
              primary: true,
              kind: model.AskActionKind.answer,
            ),
            model.AskAction('Decline', kind: model.AskActionKind.decline),
          ],
          questions: questions,
        ),
      ]),
    );

    // One field per question, and the actions that commit them together.
    expect(find.byType(Field), findsNWidgets(2));
    // The first question is also the card's title, so it appears twice: as
    // the card and as its field's label.
    expect(find.text('Which branch?'), findsNWidgets(2));
    expect(find.text('When should it land?'), findsOneWidget);

    // Nothing is sent until every question has an answer.
    await tester.enterText(find.byType(Field).first, 'next');
    await tester.pump();
    await tester.tap(find.text('Answer'));
    await tester.pump();
    expect(collected, isEmpty);

    await tester.enterText(find.byType(Field).last, 'Friday');
    await tester.pump();
    await tester.tap(find.text('Answer'));
    await tester.pump();

    expect(answers, ['q-2:Answer']);
    expect(collected.single, ['next', 'Friday']);
  });

  testWidgets('a failure shows retry', (tester) async {
    await tester.pumpWidget(
      show([
        ask(
          id: 'tc-1',
          kind: model.AskKind.approval,
          actions: ['Approve once', 'Deny'],
          state: model.AskState.failed,
          error: 'the tool call is gone',
        ),
      ]),
    );

    expect(find.text('the tool call is gone'), findsOneWidget);

    await tester.tap(find.text('Try again'));
    await tester.pump();
    // With no answer yet in this session, the retry posts the primary one.
    expect(answers, ['tc-1:Approve once']);
  });

  testWidgets('filters partition', (tester) async {
    await tester.pumpWidget(
      show([
        ask(
          id: 'tc-1',
          kind: model.AskKind.approval,
          actions: ['Approve once', 'Deny'],
        ),
        ask(
          id: 's-1',
          kind: model.AskKind.intake,
          actions: ['Start', 'Decline'],
        ),
      ]),
    );

    expect(find.byType(AskCard), findsNWidgets(2));
    expect(find.text('All'), findsOneWidget);
    expect(find.text('Blocking'), findsOneWidget);
    expect(find.text('To look at'), findsOneWidget);

    await tester.tap(find.text('To look at'));
    await tester.pump();
    expect(find.byType(AskCard), findsOneWidget);

    await tester.tap(find.text('Blocking'));
    await tester.pump();
    expect(find.byType(AskCard), findsOneWidget);
    expect(find.text('Start'), findsNothing);
  });

  testWidgets('the ready filter lists ready pull requests', (tester) async {
    final opened = <Place>[];
    await tester.pumpWidget(
      EmberApp(
        brightness: Brightness.light,
        home: Inbox(
          asks: const [],
          now: shellNow,
          pulls: [
            pullView(number: 131, title: 'Quiet capture mode'),
            // Failing is the Pull requests place's business, not the Ready
            // filter's.
            pullView(
              number: 132,
              title: 'Fix flaky calendar test',
              group: PullGroup.failing,
            ),
          ],
          onSelect: opened.add,
          onAnswer: (ask, action, {reply, answers}) {},
          onRetry: () {},
        ),
      ),
    );

    // The tab says how many wait, and only the ready one is listed.
    expect(find.text('Ready'), findsOneWidget);
    await tester.tap(find.text('Ready'));
    await tester.pump();

    expect(find.byType(PrCard), findsOneWidget);
    expect(find.byType(AskCard), findsNothing);
    expect(find.text('Quiet capture mode'), findsOneWidget);
    expect(find.text('Fix flaky calendar test'), findsNothing);
  });

  testWidgets('a work item\u2019s ask draws no action row, and opens its '
      'thread instead', (tester) async {
    final opened = <Place>[];
    await tester.pumpWidget(
      show([
        for (final (kind, label) in [
          (model.AskKind.approvalDraft, 'Approve and start'),
          (model.AskKind.decision, 'Answer'),
          (model.AskKind.recovery, 'Continue'),
          (model.AskKind.completion, 'Merge and complete'),
        ])
          ask(
            id: 'item-1:${kind.name}',
            kind: kind,
            actions: [label],
            source: model.AskSource.factory,
          ),
      ], onSelect: opened.add),
    );

    // The card is shown, and none of its actions is drawn: these four are
    // answered in the item's own thread — they carry its version and its
    // reviewed identity — so a button here would do nothing at all.
    expect(find.byType(AskCard), findsNWidgets(4));
    for (final label in const [
      'Approve and start',
      'Answer',
      'Continue',
      'Merge and complete',
    ]) {
      expect(
        find.widgetWithText(Btn, label),
        findsNothing,
        reason: '$label posts nothing from the list',
      );
    }

    // The source line is the way to the thread they are answered in.
    await tester.tap(
      find
          .text('marshal › Reliable session recovery · Factory · Everyone')
          .first,
    );
    await tester.pump();
    expect(opened.single.id, 'a-1');
  });

  testWidgets(
    'every ask has a source line, including the ones with no thread',
    (tester) async {
      await tester.pumpWidget(
        show([
          ask(
            id: 's-1',
            kind: model.AskKind.intake,
            actions: ['Start', 'Decline'],
            withThread: false,
          ),
          ask(
            id: 'r-1',
            kind: model.AskKind.reviewDraft,
            actions: ['Approve'],
            source: model.AskSource.review,
            withThread: false,
          ),
          ask(
            id: 'n-1',
            kind: model.AskKind.network,
            actions: ['Allow once'],
            source: model.AskSource.network,
            withThread: false,
          ),
          ask(
            id: 'p-1',
            kind: model.AskKind.pull,
            actions: ['Fix it'],
            withThread: false,
          ),
        ]),
      );

      // Each feed's entry says which feed it came from, and its scope. Two
      // fleet asks are two lines, not one shared guess.
      expect(find.text('Fleet · Everyone'), findsNWidgets(2));
      expect(find.text('Review bot · Everyone'), findsOneWidget);
      expect(find.text('Network · Everyone'), findsOneWidget);
      // None is drawn with a blank source line, thread or no thread.
      for (final card in tester.widgetList<AskCard>(find.byType(AskCard))) {
        expect(card.source, isNotNull);
        expect(card.source, isNotEmpty);
      }
    },
  );

  testWidgets('the blocking filter counts what the model calls blocking', (
    tester,
  ) async {
    await tester.pumpWidget(
      show([
        ask(
          id: 'tc-1',
          kind: model.AskKind.approval,
          actions: ['Approve once', 'Deny'],
        ),
        ask(
          id: 'item-1:completion',
          kind: model.AskKind.completion,
          actions: const [],
          title: 'Merge and complete',
          source: model.AskSource.factory,
        ),
      ]),
    );

    // The tab counts agree with how the model splits the two.
    final tabs = tester.widget<Tabs>(find.byType(Tabs)).tabs;
    expect(tabs.firstWhere((tab) => tab.label == 'Blocking').count, 1);
    expect(tabs.firstWhere((tab) => tab.label == 'To look at').count, 1);

    await tester.tap(find.text('Blocking'));
    await tester.pump();
    expect(find.byType(AskCard), findsOneWidget);
    expect(find.text('git push'), findsOneWidget);

    // A completion does not block work: it waits to be looked at.
    await tester.tap(find.text('To look at'));
    await tester.pump();
    expect(find.byType(AskCard), findsOneWidget);
    expect(find.text('Merge and complete'), findsOneWidget);
    expect(find.text('git push'), findsNothing);
  });

  testWidgets('an empty filter says which filter is empty', (tester) async {
    await tester.pumpWidget(
      show([
        ask(
          id: 's-1',
          kind: model.AskKind.intake,
          actions: ['Start', 'Decline'],
        ),
      ]),
    );

    await tester.tap(find.text('Blocking'));
    await tester.pump();
    expect(find.text('Nothing is blocking work.'), findsOneWidget);
    expect(find.byType(AskCard), findsNothing);

    // The way back is one action, not a dead end.
    await tester.tap(find.text('Show every ask'));
    await tester.pump();
    expect(find.byType(AskCard), findsOneWidget);
    expect(find.text('Start'), findsOneWidget);
    // The way back is gone once it is taken.
    expect(find.text('Show every ask'), findsNothing);
  });

  testWidgets('a stale source is named with its own age', (tester) async {
    await tester.pumpWidget(
      show(
        [
          ask(
            id: 'tc-1',
            kind: model.AskKind.approval,
            actions: ['Approve once', 'Deny'],
          ),
        ],
        sources: [
          SourceState(
            kind: SourceKind.fleet,
            host: 'local',
            loaded: true,
            lastRead: shellNow.subtract(const Duration(minutes: 7)),
            failureWords: 'offline',
          ),
        ],
      ),
    );

    // The row carries the read's own age and the bridge's words, not an
    // invented one. The age lives in the row's own line, so the detail does
    // not repeat it (and never says "now old").
    expect(find.text('Last updated 7m ago'), findsOneWidget);
    expect(find.textContaining('the last read failed'), findsOneWidget);
    expect(find.textContaining('offline'), findsOneWidget);
    expect(find.textContaining('now old'), findsNothing);
    expect(find.text('Last updated now ago'), findsNothing);
  });

  testWidgets('a ready pull with no checks never reads as passed', (
    tester,
  ) async {
    await tester.pumpWidget(
      show(
        const [],
        pulls: [
          pullView(number: 131, title: 'Quiet capture mode', checks: const []),
        ],
      ),
    );

    await tester.tap(find.text('Ready'));
    await tester.pump();
    expect(find.byType(PrCard), findsOneWidget);
    // Nothing pretends a check ran: the card says zero of zero passed,
    // rather than a summary that reads as every check having passed.
    expect(find.text('0 of 0 checks passed'), findsOneWidget);
    expect(find.text('2 of 2 checks passed'), findsNothing);
  });

  testWidgets('a screen called stale still marks the age when no source '
      'reports a failure', (tester) async {
    // A stream that stopped without a failed read leaves every source
    // "healthy", but the last queue is not current: §7 marks it anyway.
    await tester.pumpWidget(
      show(
        [
          ask(
            id: 'tc-1',
            kind: model.AskKind.approval,
            actions: ['Approve once', 'Deny'],
          ),
        ],
        state: StateKind.stale,
        sources: [
          SourceState(
            kind: SourceKind.fleet,
            host: 'local',
            loaded: true,
            lastRead: shellNow.subtract(const Duration(minutes: 4)),
          ),
        ],
      ),
    );

    expect(find.byType(AskCard), findsOneWidget);
    final stale = tester
        .widgetList<StateMessage>(find.byType(StateMessage))
        .where((message) => message.kind == StateKind.stale);
    expect(stale, hasLength(1));
    expect(find.text('Last updated 4m ago'), findsOneWidget);
    expect(
      find.text('The fleet has not answered; this is what it last said.'),
      findsOneWidget,
    );
  });

  testWidgets('an unreachable daemon nothing was read from says not seen yet', (
    tester,
  ) async {
    await tester.pumpWidget(show(const [], state: StateKind.unavailable));

    // No read made a snapshot, so the row says so instead of printing an
    // invented age, or "unknown ago".
    expect(find.text('The daemon is unreachable'), findsOneWidget);
    expect(find.text('Not seen yet'), findsOneWidget);
    expect(find.textContaining('ago'), findsNothing);
  });
}
