import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/inbox_model.dart';

import '../support/fake_bridge.dart';
import 'support.dart';

/// The Inbox's rules (F2 decision 7): every ask kind the fleet contract
/// carries, oldest first, answered through the bridge, kept when the answer
/// fails and gone only when the stream says so.
void main() {
  final now = DateTime.utc(2026, 11, 17, 12);

  PendingRequest approval({Map<String, Object?>? params}) => PendingRequest(
    kind: PendingKind.pendingApproval,
    id: 'tc-1',
    params: params ?? {'command': 'git push', 'reason': 'The branch is ready'},
  );

  PendingSubmission submission({String plan = ''}) => PendingSubmission(
    clientID: 'c-1',
    createdAt: now.subtract(const Duration(minutes: 30)).toIso8601String(),
    expiresAt: now.add(const Duration(hours: 1)).toIso8601String(),
    id: 's-1',
    mode: 'act',
    origin: 'tui',
    plan: plan,
    prompt: 'Fix the flaky test',
    ref: 'refs/heads/main',
    repoID: 'acme/widgets',
    title: 'Fix the flaky test',
  );

  InboxItem agentItem(String id, Duration age) => InboxItem(
    kind: InboxItemKind.inboxItemAgent,
    agentID: id,
    at: now.subtract(age).toIso8601String(),
  );

  Future<InboxModel> inboxFor(ModelFixture fixture) async {
    await fixture.refresh();
    final model = InboxModel(fixture.store, client: fixture.clientFor);
    addTearDown(model.dispose);
    return model;
  }

  test('asks are oldest first', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha')],
        agents: [
          agent(
            id: 'a-new',
            status: AgentState.agentAwaitingQuestion,
            pending: const PendingRequest(
              kind: PendingKind.pendingQuestion,
              id: 'q-new',
              params: {
                'questions': [
                  {'question': 'Which branch?'},
                ],
              },
            ),
            lastActive: now.subtract(const Duration(minutes: 2)),
          ),
          agent(
            id: 'a-old',
            status: AgentState.agentAwaitingApproval,
            pending: approval(),
            lastActive: now.subtract(const Duration(minutes: 40)),
          ),
        ],
        // The queue arrives in whatever order the bridge chose; the model is
        // what puts the oldest first.
        queue: [
          agentItem('a-new', const Duration(minutes: 2)),
          agentItem('a-old', const Duration(minutes: 40)),
        ],
      ),
    ], now: now);
    final inbox = await inboxFor(fixture);

    expect([for (final ask in inbox.asks) ask.id], ['tc-1', 'q-new']);
    expect(
      [for (final ask in inbox.asks) ask.kind],
      [AskKind.approval, AskKind.question],
    );
    expect(inbox.asks.first.since, now.subtract(const Duration(minutes: 40)));
  });

  test('approval actions', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha')],
        agents: [
          agent(
            id: 'a-1',
            status: AgentState.agentAwaitingApproval,
            pending: approval(),
          ),
        ],
      ),
    ], now: now);
    final inbox = await inboxFor(fixture);
    fixture.bridges['local']!.reply(
      'POST',
      '/api/permissions/tc-1',
      const FakeReply(200),
    );

    final ask = inbox.asks.single;
    expect(ask.kind, AskKind.approval);
    // An approval's title is the command; its body is why the agent asks.
    expect(ask.title, 'git push');
    expect(ask.body, 'The branch is ready');
    expect(ask.thread?.id, 'a-1');
    expect(
      [for (final action in ask.actions) action.label],
      ['Approve once', 'Deny'],
    );
    expect(ask.actions.first.primary, isTrue);
    expect(ask.actions.last.primary, isFalse);

    await inbox.answer(ask, ask.actions.first);

    final posted = fixture.requests('local').last;
    expect(posted.method, 'POST');
    expect(posted.uri, '/api/permissions/tc-1');
    expect(posted.body, '{"approved":true}');
    // Posted, and still shown: the stream has not confirmed it yet.
    expect(inbox.asks.single.state, AskState.sending);
  });

  test('deny posts a denial', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha')],
        agents: [
          agent(
            id: 'a-1',
            status: AgentState.agentAwaitingApproval,
            pending: approval(),
          ),
        ],
      ),
    ], now: now);
    final inbox = await inboxFor(fixture);
    fixture.bridges['local']!.reply(
      'POST',
      '/api/permissions/tc-1',
      const FakeReply(200),
    );

    final ask = inbox.asks.single;
    await inbox.answer(ask, ask.actions[1]);

    final posted = fixture.requests('local').last;
    expect(posted.uri, '/api/permissions/tc-1');
    expect(posted.body, '{"approved":false}');
  });

  test('question actions', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha')],
        agents: [
          agent(
            id: 'a-1',
            status: AgentState.agentAwaitingQuestion,
            pending: const PendingRequest(
              kind: PendingKind.pendingQuestion,
              id: 'q-1',
              params: {
                'questions': [
                  {
                    'question': 'Which branch?',
                    'options': [
                      {'label': 'main', 'value': 'main'},
                      {'label': 'next', 'value': 'release/next'},
                    ],
                  },
                ],
              },
            ),
          ),
        ],
      ),
    ], now: now);
    final inbox = await inboxFor(fixture);
    fixture.bridges['local']!.reply(
      'POST',
      '/api/questions/q-1',
      const FakeReply(200),
    );

    final ask = inbox.asks.single;
    expect(ask.kind, AskKind.question);
    expect(ask.title, 'Which branch?');
    // Its own options, then a free reply.
    expect(
      [for (final action in ask.actions) action.label],
      ['main', 'next', 'Reply'],
    );
    expect(ask.actions.first.primary, isTrue);

    // An option answers with its wire value, which need not be its label.
    await inbox.answer(ask, ask.actions[1]);
    expect(
      fixture.requests('local').last.body,
      '{"answers":[{"question":"Which branch?","answer":"release/next"}]}',
    );

    // A reply answers with the free text.
    await inbox.answer(ask, ask.actions.last, reply: '  a topic branch  ');
    expect(
      fixture.requests('local').last.body,
      '{"answers":[{"question":"Which branch?","answer":"a topic branch"}]}',
    );
  });

  /// Two questions in one request, which the agent answers as a pair: it
  /// requires exactly one Answer per Question, in order, and reads anything
  /// else as a decline (internal/acp/questions.go).
  Future<InboxModel> twoQuestions(ModelFixture fixture) async {
    fixture.serve('local', '/api/agents', [
      agent(
        id: 'a-1',
        status: AgentState.agentAwaitingQuestion,
        pending: const PendingRequest(
          kind: PendingKind.pendingQuestion,
          id: 'q-2',
          params: {
            'questions': [
              {
                'question': 'Which branch?',
                'options': [
                  {'label': 'main', 'value': 'main'},
                  {'label': 'next', 'value': 'release/next'},
                ],
              },
              {'question': 'When should it land?'},
            ],
          },
        ),
      ).toJson(),
    ]);
    await fixture.refresh();
    final model = InboxModel(fixture.store, client: fixture.clientFor);
    addTearDown(model.dispose);
    return model;
  }

  test('a two-question ask posts one answer per question, in order', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(name: 'local', projects: [project('/home/u/alpha')]),
    ], now: now);
    final inbox = await twoQuestions(fixture);
    fixture.bridges['local']!.reply(
      'POST',
      '/api/questions/q-2',
      const FakeReply(200),
    );

    final ask = inbox.asks.single;
    expect(ask.questions, hasLength(2));
    expect(
      [for (final q in ask.questions) q.text],
      ['Which branch?', 'When should it land?'],
    );
    // One action commits both questions; a decline says so outright rather
    // than posting a shape the agent would read as one.
    expect(
      [for (final action in ask.actions) action.label],
      ['Answer', 'Decline'],
    );
    expect(ask.actions.first.primary, isTrue);
    expect(ask.body, '1 more question');

    await inbox.answer(ask, ask.actions.first, answers: ['main', 'Friday']);

    expect(
      fixture.requests('local').last.body,
      '{"answers":['
      '{"question":"Which branch?","answer":"main"},'
      '{"question":"When should it land?","answer":"Friday"}'
      ']}',
      reason: 'exactly one Answer per Question, in the request’s order',
    );
    expect(inbox.asks.single.state, AskState.sending);
  });

  test('a collected answer names the option’s wire value', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(name: 'local', projects: [project('/home/u/alpha')]),
    ], now: now);
    final inbox = await twoQuestions(fixture);
    fixture.bridges['local']!.reply(
      'POST',
      '/api/questions/q-2',
      const FakeReply(200),
    );

    final ask = inbox.asks.single;
    await inbox.answer(ask, ask.actions.first, answers: ['next', 'Friday']);

    expect(
      fixture.requests('local').last.body,
      '{"answers":['
      '{"question":"Which branch?","answer":"release/next"},'
      '{"question":"When should it land?","answer":"Friday"}'
      ']}',
      reason:
          'the label a person reads is not always the value the agent reads',
    );
  });

  test('a collected answer that misses a question is not posted', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(name: 'local', projects: [project('/home/u/alpha')]),
    ], now: now);
    final inbox = await twoQuestions(fixture);
    final before = fixture.requests('local').length;

    final ask = inbox.asks.single;
    // One answer for two questions, no answers at all, and a blank one: each
    // would be read as every question declined, so none is sent.
    await inbox.answer(ask, ask.actions.first, answers: ['main']);
    await inbox.answer(ask, ask.actions.first);
    await inbox.answer(ask, ask.actions.first, answers: ['main', '   ']);

    expect(fixture.requests('local'), hasLength(before));
    expect(inbox.asks.single.state, AskState.waiting);
  });

  test('declining a two-question ask says so outright', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(name: 'local', projects: [project('/home/u/alpha')]),
    ], now: now);
    final inbox = await twoQuestions(fixture);
    fixture.bridges['local']!.reply(
      'POST',
      '/api/questions/q-2',
      const FakeReply(200),
    );

    final ask = inbox.asks.single;
    await inbox.answer(ask, ask.actions.last);

    expect(fixture.requests('local').last.body, '{"declined":true}');
  });

  test('a reply with no words is not sent', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha')],
        agents: [
          agent(
            id: 'a-1',
            status: AgentState.agentAwaitingQuestion,
            pending: const PendingRequest(
              kind: PendingKind.pendingQuestion,
              id: 'q-1',
              params: {
                'questions': [
                  {'question': 'Which branch?'},
                ],
              },
            ),
          ),
        ],
      ),
    ], now: now);
    final inbox = await inboxFor(fixture);
    final before = fixture.requests('local').length;

    final ask = inbox.asks.single;
    await inbox.answer(ask, ask.actions.single, reply: '   ');

    expect(fixture.requests('local'), hasLength(before));
    expect(inbox.asks.single.state, AskState.waiting);
  });

  test('intake actions', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        queue: [
          InboxItem(
            kind: InboxItemKind.inboxItemIntake,
            submission: submission(plan: '## Task 1\n\n### Task 2\n'),
            at: now.subtract(const Duration(minutes: 5)).toIso8601String(),
          ),
        ],
      ),
    ], now: now);
    final inbox = await inboxFor(fixture);
    fixture.bridges['local']!.reply(
      'POST',
      '/api/pending/s-1/deny',
      const FakeReply(200, body: '{"status":"denied"}'),
    );

    final ask = inbox.asks.single;
    expect(ask.kind, AskKind.intake);
    expect(ask.title, 'Fix the flaky test');
    // A submission has no agent yet, so no thread.
    expect(ask.thread, isNull);
    // Its scale, so the operator can judge it before starting.
    expect(ask.body, 'acme/widgets · plan · 2 tasks');
    expect(
      [for (final action in ask.actions) action.label],
      ['Start', 'Decline'],
    );
    expect(ask.actions.first.primary, isTrue);

    await inbox.answer(ask, ask.actions.last);
    expect(fixture.requests('local').last.uri, '/api/pending/s-1/deny');
  });

  test('a failed answer keeps the ask', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha')],
        agents: [
          agent(
            id: 'a-1',
            status: AgentState.agentAwaitingApproval,
            pending: approval(),
          ),
        ],
      ),
    ], now: now);
    final inbox = await inboxFor(fixture);
    fixture.bridges['local']!.reply(
      'POST',
      '/api/permissions/tc-1',
      const FakeReply(500, body: '{"error":"the tool call is gone"}'),
    );

    final ask = inbox.asks.single;
    await inbox.answer(ask, ask.actions.first);

    final failed = inbox.asks.single;
    expect(failed.state, AskState.failed);
    expect(failed.error, 'the tool call is gone');
    expect(
      [for (final action in failed.actions) action.label],
      ['Approve once', 'Deny'],
      reason: 'the same actions are how the answer is retried',
    );
  });

  test('an ask leaves when the stream confirms', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha')],
        agents: [
          agent(
            id: 'a-1',
            status: AgentState.agentAwaitingApproval,
            pending: approval(),
          ),
        ],
        queue: [agentItem('a-1', const Duration(minutes: 5))],
      ),
    ], now: now);
    final inbox = await inboxFor(fixture);
    fixture.bridges['local']!.reply(
      'POST',
      '/api/permissions/tc-1',
      const FakeReply(200),
    );
    expect(inbox.asks, hasLength(1));

    await inbox.answer(inbox.asks.single, inbox.asks.single.actions.first);
    expect(inbox.asks, hasLength(1), reason: 'a sent answer is not done work');

    // The agent's request is answered: the snapshot no longer reports it, and
    // the bridge's queue is empty.
    fixture.serve('local', '/api/agents', [
      agent(id: 'a-1', status: AgentState.agentRunning).toJson(),
    ]);
    fixture.serve('local', '/api/inbox', {
      'groups': {
        'needsYou': [],
        'running': ['a-1'],
        'ready': [],
        'earlier': [],
      },
      'queue': <Object>[],
    });
    await fixture.refresh();

    expect(inbox.asks, isEmpty);
  });

  test('a failed outcome becomes an ask', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        queue: [
          InboxItem(
            kind: InboxItemKind.inboxItemPull,
            pull: PullAsk(
              pull: 'acme/widgets#131',
              check: 'unit',
              outcome: 'forbidden_change',
              tried: 'it wanted to delete the failing test',
              finding: 'the test is right and the code is wrong',
              action: 'Fix it by hand, or ask a worker with a note',
              at: now.millisecondsSinceEpoch,
            ),
            at: now.subtract(const Duration(minutes: 5)).toIso8601String(),
          ),
        ],
      ),
    ], now: now);

    final inbox = await inboxFor(fixture);
    final ask = inbox.asks.single;

    expect(ask.kind, AskKind.pull);
    expect(ask.title, '#131 · the fix was refused as a forbidden change');
    expect(ask.body, contains('unit'));
    expect(ask.body, contains('the test is right and the code is wrong'));
    expect(
      [for (final action in ask.actions) action.label],
      ['Open', 'Try again', 'Dismiss'],
    );
    expect(
      [for (final action in ask.actions) action.kind],
      [AskActionKind.open, AskActionKind.retry, AskActionKind.dismiss],
      reason: 'the first action is the primary one, and it comes first',
    );
    expect(ask.actions.first.primary, isTrue);
  });

  test('trying again asks a worker for the same check', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        queue: [
          InboxItem(
            kind: InboxItemKind.inboxItemPull,
            pull: PullAsk(
              pull: 'acme/widgets#131',
              check: 'unit',
              outcome: 'gave_up',
              at: now.millisecondsSinceEpoch,
            ),
            at: now.subtract(const Duration(minutes: 5)).toIso8601String(),
          ),
        ],
      ),
    ], now: now);
    final inbox = await inboxFor(fixture);
    fixture.bridges['local']!.reply(
      'POST',
      '/api/pulls/${Uri.encodeComponent('acme/widgets#131')}/fix',
      const FakeReply(202, body: '{}'),
    );

    final ask = inbox.asks.single;
    final retry = ask.actions[1];
    await inbox.answer(ask, retry);

    final posted = fixture
        .requests('local')
        .where((r) => r.method == 'POST' && r.uri.contains('/fix'))
        .toList();
    expect(posted, hasLength(1));
    expect(posted.single.body, contains('"check":"unit"'));
  });

  test('dismissing a pull request ask closes it here', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        queue: [
          InboxItem(
            kind: InboxItemKind.inboxItemPull,
            pull: PullAsk(
              pull: 'acme/widgets#131',
              check: 'unit',
              outcome: 'gave_up',
              at: now.millisecondsSinceEpoch,
            ),
            at: now.subtract(const Duration(minutes: 5)).toIso8601String(),
          ),
        ],
      ),
    ], now: now);
    final inbox = await inboxFor(fixture);
    final ask = inbox.asks.single;

    await inbox.answer(ask, ask.actions[2]);

    expect(inbox.asks, isEmpty, reason: 'the bridge has no dismissal route');
    expect(
      fixture.requests('local').where((r) => r.method != 'GET'),
      isEmpty,
      reason: 'nothing is posted for a dismissal',
    );
  });
}
