import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/attention.dart' as model;
import 'package:marshal_app/src/model/inbox_model.dart' as model;
import 'package:marshal_app/src/model/thread_ref.dart' as model;
import 'package:marshal_app/src/ui/inbox/answerable_ask.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

/// The moment the ask was made, and the clock the card reads it by.
final _asked = DateTime.utc(2026, 11, 17, 4, 10);
final _now = DateTime.utc(2026, 11, 17, 4, 22);

model.Ask _ask({
  String id = 'call-1',
  model.AskKind kind = model.AskKind.approval,
  String title = 'Approve shell.run?',
  List<model.AskAction> actions = const [
    model.AskAction('Approve once', primary: true),
    model.AskAction('Deny'),
  ],
  List<model.AskQuestion> questions = const [],
  model.AskState state = model.AskState.waiting,
  String? error,
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
    lastActive: _asked,
  ),
  title: title,
  body: '',
  actions: actions,
  questions: questions,
  since: _asked,
  source: model.AskSource.fleet,
  scope: model.AskScope.everyone,
  state: state,
  error: error,
);

/// One ask, drawn the way the place that holds it draws it.
Widget _card(
  model.Ask ask, {
  AskPlacement placement = AskPlacement.inbox,
  AnswerAsk? onAnswer,
  VoidCallback? onOpenInbox,
}) => EmberApp(
  brightness: Brightness.light,
  home: AnswerableAsk(
    ask: ask,
    placement: placement,
    now: _now,
    onAnswer: onAnswer,
    onOpenInbox: onOpenInbox,
  ),
);

void main() {
  testWidgets('Reply opens a field and posts its words', (tester) async {
    String? postedReply;
    model.AskAction? withAction;
    await tester.pumpWidget(
      _card(
        _ask(
          kind: model.AskKind.question,
          title: 'Which branch?',
          actions: const [
            model.AskAction(
              'Reply',
              primary: true,
              kind: model.AskActionKind.reply,
            ),
            model.AskAction('Decline', kind: model.AskActionKind.decline),
          ],
        ),
        onAnswer: (ask, action, {reply, answers}) {
          withAction = action;
          postedReply = reply;
        },
      ),
    );

    // Nothing to type into until the reply is asked for.
    expect(find.byType(Field), findsNothing);
    await tester.tap(find.text('Reply'));
    await tester.pump();
    expect(find.byType(Field), findsOneWidget);

    await tester.enterText(find.byType(Field), 'a topic branch');
    await tester.tap(find.text('Send'));
    await tester.pump();

    // The words go with the action, and the field closes behind them.
    expect(withAction?.label, 'Reply');
    expect(postedReply, 'a topic branch');
    expect(find.byType(Field), findsNothing);
  });

  testWidgets('a multi-question ask posts one answer per question', (
    tester,
  ) async {
    List<String>? collected;
    await tester.pumpWidget(
      _card(
        _ask(
          kind: model.AskKind.question,
          title: 'Which branch?',
          questions: const [
            model.AskQuestion(
              text: 'Which branch?',
              options: [
                model.AskOption('main'),
                model.AskOption('next', value: 'release/next'),
              ],
            ),
            model.AskQuestion(text: 'When should it land?'),
          ],
          actions: const [
            model.AskAction(
              'Answer',
              primary: true,
              kind: model.AskActionKind.answer,
            ),
            model.AskAction('Decline', kind: model.AskActionKind.decline),
          ],
        ),
        onAnswer: (ask, action, {reply, answers}) => collected = answers,
      ),
    );

    // One field per question.
    expect(find.byType(Field), findsNWidgets(2));

    // Nothing is sent until every question has an answer.
    await tester.enterText(find.byType(Field).first, 'next');
    await tester.pump();
    await tester.tap(find.text('Answer'));
    await tester.pump();
    expect(collected, isNull, reason: 'one answer is not the whole request');

    await tester.enterText(find.byType(Field).last, 'Friday');
    await tester.pump();
    await tester.tap(find.text('Answer'));
    await tester.pump();

    expect(collected, ['next', 'Friday']);
  });

  testWidgets('a failed answer shows why and tries the same answer again', (
    tester,
  ) async {
    final posted = <String>[];
    void record(
      model.Ask ask,
      model.AskAction action, {
      String? reply,
      List<String>? answers,
    }) => posted.add(action.label);
    final ask = _ask();
    await tester.pumpWidget(_card(ask, onAnswer: record));

    await tester.tap(find.text('Deny'));
    await tester.pump();
    expect(posted, ['Deny']);

    // The bridge refused it: the card keeps its words, says why, and offers
    // the one action a failure offers.
    await tester.pumpWidget(
      _card(
        ask.withState(model.AskState.failed, error: 'the tool call is gone'),
        onAnswer: record,
      ),
    );
    expect(find.text('the tool call is gone'), findsOneWidget);

    await tester.tap(find.text('Try again'));
    await tester.pump();

    // The same answer again, not the card's primary one.
    expect(posted, ['Deny', 'Deny']);
  });

  testWidgets('the Overview sends a multi-question ask to Needs you', (
    tester,
  ) async {
    var opened = 0;
    await tester.pumpWidget(
      _card(
        _ask(
          kind: model.AskKind.question,
          title: 'Which branch?',
          questions: const [
            model.AskQuestion(
              text: 'Which branch?',
              options: [model.AskOption('main')],
            ),
            model.AskQuestion(text: 'When should it land?'),
          ],
          actions: const [
            model.AskAction(
              'Answer',
              primary: true,
              kind: model.AskActionKind.answer,
            ),
          ],
        ),
        placement: AskPlacement.overview,
        onAnswer: (ask, action, {reply, answers}) {},
        onOpenInbox: () => opened++,
      ),
    );

    // The Overview has no room to collect one answer per question, so it
    // offers no field here and one way to the place that does.
    expect(find.byType(Field), findsNothing);
    expect(find.byType(AskCard), findsOneWidget);
    expect(find.text('Open Needs you'), findsOneWidget);

    await tester.tap(find.text('Open Needs you'));
    await tester.pump();
    expect(opened, 1);
  });
}
