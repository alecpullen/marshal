import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/inbox_model.dart' as model;
import 'package:marshal_app/src/model/thread_ref.dart' as model;
import 'package:marshal_app/src/ui/inbox/inbox.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

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
    Map<String, Object?>? params,
  }) => model.Ask(
    host: 'local',
    id: id,
    kind: kind,
    thread: model.ThreadRef(
      host: 'local',
      id: 'a-1',
      project: '/home/u/marshal',
      title: 'Reliable session recovery',
      kind: model.ThreadKind.agent,
      status: model.ThreadStatus.needsYou,
      headline: '',
      lastActive: shellNow,
    ),
    title: kind == model.AskKind.question ? 'Which branch?' : 'git push',
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
    state: state,
    error: error,
  );

  /// The answers posted, as "ask id:label": the list the widget calls back
  /// into, named once here so a closure's own `answers` can shadow it.
  final posted = answers;

  Widget show(List<model.Ask> asks, {StateKind? state}) => EmberApp(
    brightness: Brightness.light,
    home: Inbox(
      asks: asks,
      now: shellNow,
      state: state,
      onSelect: (_) {},
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
}
