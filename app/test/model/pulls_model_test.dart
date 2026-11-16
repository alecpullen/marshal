import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/pulls_model.dart';
import 'package:marshal_app/src/model/pulls_store.dart';
import 'package:marshal_app/src/model/thread_ref.dart';

import '../support/fake_bridge.dart';
import 'support.dart';

/// The pull requests' rules (F5 decisions 10–12, 15): the groups, the one-line
/// summaries, and the one click that dispatches a worker.
void main() {
  PullRecord record({
    int number = 1,
    PullState state = PullState.pullFailing,
    List<PullCheck> checks = const [
      PullCheck(name: 'build', state: CheckState.checkPassed),
      PullCheck(name: 'unit', state: CheckState.checkFailed),
    ],
    String? unavailable,
    PullFix? fix,
    PullOrigin origin = PullOrigin.pullOriginAgent,
    String? agentID = 'a-1',
    String? factoryItem,
  }) => PullRecord(
    id: 'acme/widgets#$number',
    repoID: 'acme/widgets',
    projectRoot: '/home/u/alpha',
    number: number,
    url: 'https://forge.example/acme/widgets/pull/$number',
    title: 'Quiet capture mode',
    headRef: 'agent/quiet-capture',
    baseRef: 'main',
    headSHA: 'a1b2c3d4',
    draft: false,
    additions: 412,
    deletions: 96,
    origin: origin,
    agentID: agentID,
    factoryItem: factoryItem,
    state: state,
    checks: checks,
    reviews: '2 reviews passed',
    unavailable: unavailable,
    fix: fix,
    observedAt: 1000,
    updatedAt: 1000,
  );

  /// A model over one host with [records] at its index.
  Future<(ModelFixture, PullsStore, PullsModel)> modelFor(
    List<PullRecord> records,
  ) async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha')],
        agents: [agent(id: 'a-1')],
      ),
    ]);
    await fixture.refresh();
    fixture.serve('local', '/api/pulls', {
      'pulls': [for (final r in records) r.toJson()],
    });
    final store = PullsStore(fixture.registry, fixture.store);
    addTearDown(store.dispose);
    await eventually(() => store.all().isNotEmpty);
    final model = PullsModel(store, client: fixture.clientFor);
    addTearDown(model.dispose);
    return (fixture, store, model);
  }

  test('groups follow state', () async {
    final (_, _, model) = await modelFor([
      record(number: 1, state: PullState.pullReady),
      record(number: 2, state: PullState.pullFailing),
      record(number: 3, state: PullState.pullBeingFixed),
      record(number: 4, state: PullState.pullInProgress),
      record(number: 5, state: PullState.pullChecksUnavailable),
    ]);

    final numbers = {
      for (final group in PullGroup.values)
        group: [for (final view in model.groups[group]!) view.record.number],
    };
    expect(numbers, {
      PullGroup.ready: [1],
      PullGroup.failing: [2],
      PullGroup.beingFixed: [3],
      PullGroup.inProgress: [4],
      PullGroup.unavailable: [5],
    });
    expect(model.readyCount, 1);
    expect(PullGroup.values, [
      PullGroup.ready,
      PullGroup.failing,
      PullGroup.beingFixed,
      PullGroup.inProgress,
      PullGroup.unavailable,
    ], reason: 'the place lists them in this order (decision 10)');
  });

  test('summary for each state', () {
    expect(
      PullsModel.summaryOf(
        record(
          checks: const [
            PullCheck(name: 'build', state: CheckState.checkPassed),
            PullCheck(name: 'vet', state: CheckState.checkPassed),
            PullCheck(name: 'lint', state: CheckState.checkPassed),
          ],
        ),
      ),
      '3 of 3 checks passed',
    );
    expect(PullsModel.summaryOf(record()), '1 failing: unit');
    expect(
      PullsModel.summaryOf(
        record(
          state: PullState.pullInProgress,
          checks: const [
            PullCheck(name: 'build', state: CheckState.checkPassed),
            PullCheck(name: 'unit', state: CheckState.checkRunning),
            PullCheck(name: 'lint', state: CheckState.checkPending),
          ],
        ),
      ),
      '1 of 3 checks passed, the rest running',
    );
    expect(
      PullsModel.summaryOf(
        record(
          state: PullState.pullChecksUnavailable,
          checks: const [],
          unavailable:
              'the credential is an SSH key, which cannot call the API',
        ),
      ),
      'Checks unavailable: the credential is an SSH key, which cannot call '
      'the API',
    );
    expect(
      PullsModel.summaryOf(record(state: PullState.pullInProgress, checks: [])),
      'No checks on this head yet',
    );
  });

  test('fix posts the failing check', () async {
    final (fixture, store, model) = await modelFor([record()]);
    final id = 'acme/widgets#1';
    final path = '/api/pulls/${Uri.encodeComponent(id)}/fix';
    fixture.bridges['local']!.reply(
      'POST',
      path,
      FakeReply(
        202,
        headers: const {'content-type': 'application/json'},
        body: jsonEncode(
          record(
            state: PullState.pullBeingFixed,
            fix: const PullFix(
              id: 'f-1',
              check: 'unit',
              headSHA: 'a1b2c3d4',
              mode: 'fix',
              startedAt: 2000,
            ),
          ).toJson(),
        ),
      ),
    );

    await model.fix(model.groups[PullGroup.failing]!.single);

    final posted = fixture
        .requests('local')
        .where((r) => r.method == 'POST' && r.uri.endsWith('/fix'))
        .toList();
    expect(posted, hasLength(1), reason: 'one click, one dispatch');
    expect(jsonDecode(posted.single.body), {'check': 'unit', 'mode': 'fix'});
    expect(
      store.byId('local', id)?.state,
      PullState.pullBeingFixed,
      reason: 'an accepted dispatch is shown as accepted, not as fixed',
    );
    expect(model.groups[PullGroup.beingFixed], hasLength(1));
  });

  test('a refused fix shows the running one', () async {
    final (fixture, store, model) = await modelFor([record()]);
    final id = 'acme/widgets#1';
    final encoded = Uri.encodeComponent(id);
    final running = record(
      state: PullState.pullBeingFixed,
      fix: const PullFix(
        id: 'f-running',
        check: 'unit',
        headSHA: 'a1b2c3d4',
        mode: 'fix',
        startedAt: 900,
      ),
    );
    fixture.bridges['local']!
      ..reply(
        'POST',
        '/api/pulls/$encoded/fix',
        const FakeReply(
          409,
          headers: {'content-type': 'application/json'},
          body: '{"error":"a fix is already running","fixId":"f-running"}',
        ),
      )
      ..reply(
        'GET',
        '/api/pulls/$encoded',
        FakeReply(
          200,
          headers: const {'content-type': 'application/json'},
          body: jsonEncode(running.toJson()),
        ),
      );

    await model.fix(model.groups[PullGroup.failing]!.single);

    final view = model.views.single;
    expect(view.error, isNull, reason: 'a refusal that names a fix is not one');
    expect(view.record.fix?.id, 'f-running');
    expect(
      view.group,
      PullGroup.beingFixed,
      reason: 'the running fix is what a person sees',
    );
    expect(store.byId('local', id)?.fix?.id, 'f-running');
  });

  test('a refused fix keeps the view with the bridge words', () async {
    final (fixture, _, model) = await modelFor([record()]);
    fixture.bridges['local']!.reply(
      'POST',
      '/api/pulls/${Uri.encodeComponent('acme/widgets#1')}/fix',
      const FakeReply(
        400,
        headers: {'content-type': 'application/json'},
        body: '{"error":"that check is not failing"}',
      ),
    );

    await model.fix(model.groups[PullGroup.failing]!.single);

    final view = model.views.single;
    expect(view.error, 'that check is not failing');
    expect(view.sending, isFalse);
    expect(view.group, PullGroup.failing, reason: 'the view is kept');
  });

  test('stop clears the fix', () async {
    final (fixture, store, model) = await modelFor([
      record(
        state: PullState.pullBeingFixed,
        fix: const PullFix(
          id: 'f-1',
          check: 'unit',
          headSHA: 'a1b2c3d4',
          mode: 'fix',
          startedAt: 900,
        ),
      ),
    ]);
    final encoded = Uri.encodeComponent('acme/widgets#1');
    fixture.bridges['local']!
      ..reply('POST', '/api/pulls/$encoded/fix/stop', const FakeReply(200))
      ..reply(
        'GET',
        '/api/pulls/$encoded',
        FakeReply(
          200,
          headers: const {'content-type': 'application/json'},
          body: jsonEncode(record().toJson()),
        ),
      );

    await model.stopFix(model.groups[PullGroup.beingFixed]!.single);

    expect(model.groups[PullGroup.beingFixed], isEmpty);
    expect(store.byId('local', 'acme/widgets#1')?.fix, isNull);
    expect(model.groups[PullGroup.failing], hasLength(1));
  });

  test('a note rides the dispatch', () async {
    final (fixture, _, model) = await modelFor([record()]);
    fixture.bridges['local']!.reply(
      'POST',
      '/api/pulls/${Uri.encodeComponent('acme/widgets#1')}/fix',
      const FakeReply(202, body: '{}'),
    );

    await model.fix(
      model.groups[PullGroup.failing]!.single,
      note: 'the clock is pinned in the fixture',
    );

    final posted = fixture
        .requests('local')
        .lastWhere((r) => r.method == 'POST' && r.uri.endsWith('/fix'));
    expect(jsonDecode(posted.body), {
      'check': 'unit',
      'mode': 'fix',
      'note': 'the clock is pinned in the fixture',
    });
  });

  test('investigate only is the same dispatch with its own mode', () async {
    final (fixture, _, model) = await modelFor([record()]);
    fixture.bridges['local']!.reply(
      'POST',
      '/api/pulls/${Uri.encodeComponent('acme/widgets#1')}/fix',
      const FakeReply(202, body: '{}'),
    );

    await model.fix(
      model.groups[PullGroup.failing]!.single,
      mode: 'investigate',
    );

    final posted = fixture
        .requests('local')
        .lastWhere((r) => r.method == 'POST' && r.uri.endsWith('/fix'));
    expect(jsonDecode(posted.body), {'check': 'unit', 'mode': 'investigate'});
  });

  test('auto-fix changes the project setting', () async {
    final (fixture, _, model) = await modelFor([record()]);
    fixture.bridges['local']!
      ..reply(
        'GET',
        '/api/projects/settings',
        const FakeReply(
          200,
          headers: {'content-type': 'application/json'},
          body:
              '{"workspace":"default","automations":{"ciFixer":'
              '{"enabled":true,"repoId":"acme/widgets"}}}',
        ),
      )
      ..reply('PUT', '/api/projects/settings', const FakeReply(200));

    await model.setAutoFix('/home/u/alpha', true);

    final put = fixture.requests('local').lastWhere((r) => r.method == 'PUT');
    final body = jsonDecode(put.body) as Map<String, dynamic>;
    expect(body['workspace'], 'default', reason: 'the document is kept whole');
    expect((body['automations'] as Map)['autoFix'], isTrue);
    expect(
      ((body['automations'] as Map)['ciFixer'] as Map)['enabled'],
      isTrue,
      reason: 'the other automations survive the write',
    );
  });

  test('send back reaches the agent that shipped it', () async {
    final (fixture, _, model) = await modelFor([record()]);
    fixture.bridges['local']!
      ..reply('POST', '/api/sessions/a-1/prompt', const FakeReply(200))
      ..reply('POST', '/api/sessions/a-1/steer', const FakeReply(200));
    final pull = model.views.single;
    expect(pull.thread, isNotNull, reason: 'the fleet still lists the agent');

    // An agent that has finished its turn is asked again.
    await model.sendBack(pull, text: 'pin the clock in the fixture');
    final first = fixture
        .requests('local')
        .lastWhere((r) => r.method == 'POST');
    expect(first.uri, contains('/prompt'));
    expect(jsonDecode(first.body), {'text': 'pin the clock in the fixture'});

    // A turn in flight takes it as steering instead.
    fixture.serve('local', '/api/agents', [
      agent(id: 'a-1', status: AgentState.agentRunning).toJson(),
    ]);
    await fixture.refresh();
    await eventually(
      () => model.views.single.thread?.status == ThreadStatus.running,
      reason: 'the agent is running',
    );
    await model.sendBack(model.views.single, text: 'and nothing else');
    final second = fixture
        .requests('local')
        .lastWhere((r) => r.method == 'POST');
    expect(second.uri, contains('/steer'));
    expect(jsonDecode(second.body), {'text': 'and nothing else'});
  });

  test('a merge posts the agent route', () async {
    final (fixture, _, model) = await modelFor([record()]);
    fixture.bridges['local']!.reply(
      'POST',
      '/api/agents/a-1/merge',
      const FakeReply(200, body: '{"merged":true}'),
    );

    await model.merge(model.views.single);

    final merged = fixture
        .requests('local')
        .lastWhere((r) => r.method == 'POST');
    expect(merged.uri, contains('/api/agents/a-1/merge'));
  });

  test('a factory item’s merge is refused, and posts nothing', () async {
    // The factory's own command names the reviewed node id, and a pull record
    // carries none: the bridge refuses a blank one with 400, so there is no
    // request worth making (F5 review; F6 brings the reviewed identity).
    final (fixture, _, model) = await modelFor([
      record(
        state: PullState.pullReady,
        origin: PullOrigin.pullOriginFactory,
        agentID: null,
        factoryItem: 'item-7',
      ),
    ]);

    await expectLater(
      model.merge(model.views.single),
      throwsA(isA<StateError>()),
    );

    expect(
      fixture.requests('local').where((r) => r.method != 'GET'),
      isEmpty,
      reason: 'nothing is posted for a merge this build cannot make',
    );
  });
}
