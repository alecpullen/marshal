import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/selection.dart';
import 'package:marshal_app/src/model/stack_store.dart';

/// A store whose three rows say what the selection copies.
StackStore _store() => StackStore.seeded(
  const StackSnapshot(
    sessionID: 's-1',
    rev: 1,
    roots: ['turn:1'],
    nodes: [
      WireNode(
        id: 'turn:1',
        kind: 'turn',
        children: ['msg:1', 'step:1', 'step:2'],
      ),
      WireNode(
        id: 'msg:1',
        kind: 'message',
        parent: 'turn:1',
        message: WireMessage(
          role: 'assistant',
          content: 'The parser is the first stop.',
        ),
      ),
      WireNode(
        id: 'step:1',
        kind: 'step',
        parent: 'turn:1',
        step: WireStep(headline: 'Mapped the fixtures'),
      ),
      WireNode(
        id: 'step:2',
        kind: 'step',
        parent: 'turn:1',
        step: WireStep(headline: 'Removed the dead ones'),
      ),
    ],
  ),
);

void main() {
  late StackStore store;
  late TranscriptSelection selection;
  const order = ['msg:1', 'step:1', 'step:2'];

  setUp(() {
    store = _store();
    selection = TranscriptSelection(textOf: store.plainText);
  });

  tearDown(() {
    selection.dispose();
    store.dispose();
  });

  test('a drag across rows copies them in order, on their own lines', () {
    selection.begin(const TextAnchor('msg:1', 4));
    selection.extend(const TextAnchor('step:2', 7));

    expect(
      selection.copy(store, order),
      'parser is the first stop.\n'
      'Mapped the fixtures\n'
      'Removed',
    );
  });

  test('a selection made backwards copies forwards', () {
    selection.begin(const TextAnchor('step:2', 7));
    selection.extend(const TextAnchor('msg:1', 4));

    expect(selection.copy(store, order), startsWith('parser'));
    expect(selection.copy(store, order), endsWith('Removed'));
  });

  test('rows that were never built still copy', () {
    // The two anchors are the first and last row; the middle row was never
    // on screen, which is exactly what model-backed anchors allow.
    selection.begin(const TextAnchor('msg:1', 0));
    selection.extend(const TextAnchor('step:2', 21));

    expect(selection.copy(store, order), contains('Mapped the fixtures'));
  });

  test('an empty selection copies nothing', () {
    expect(selection.copy(store, order), isEmpty);
    expect(selection.range(order), isNull);
  });

  test('clearing drops both anchors', () {
    selection.begin(const TextAnchor('msg:1', 0));
    selection.clear();
    expect(selection.isEmpty, isTrue);
    expect(selection.copy(store, order), isEmpty);
  });

  test('selecting a word takes it whole', () {
    // "parser" sits at 4..10 in "The parser is the first stop."
    selection.selectWord(const TextAnchor('msg:1', 6));
    expect(selection.base, const TextAnchor('msg:1', 4));
    expect(selection.extent, const TextAnchor('msg:1', 10));
    expect(selection.copy(store, order), 'parser');
  });

  test('a click on the space between words takes the word after it', () {
    selection.selectWord(const TextAnchor('msg:1', 3));
    expect(selection.copy(store, order), 'parser');
  });

  test('selecting a line takes it whole', () {
    selection.selectLine(const TextAnchor('step:1', 3));
    expect(selection.copy(store, order), 'Mapped the fixtures');
  });

  test('select all copies every row in order', () {
    selection.selectAll(order);
    expect(
      selection.copy(store, order),
      'The parser is the first stop.\n'
      'Mapped the fixtures\n'
      'Removed the dead ones',
    );
  });

  test('an anchor whose node left the list copies nothing', () {
    selection.begin(const TextAnchor('gone:1', 0));
    selection.extend(const TextAnchor('step:1', 3));
    expect(selection.copy(store, order), isEmpty);
  });

  test('spanFor reports the part of one row the selection covers', () {
    selection.begin(const TextAnchor('msg:1', 4));
    selection.extend(const TextAnchor('step:2', 7));

    expect(selection.spanFor('msg:1', order, store.plainText('msg:1')), (
      4,
      29,
    ));
    expect(selection.spanFor('step:1', order, store.plainText('step:1')), (
      0,
      19,
    ));
    expect(selection.spanFor('step:2', order, store.plainText('step:2')), (
      0,
      7,
    ));
  });

  test('a change notifies, and an unchanged extent does not', () {
    var notes = 0;
    selection.addListener(() => notes++);

    selection.begin(const TextAnchor('msg:1', 0));
    selection.begin(const TextAnchor('msg:1', 0));
    selection.extend(const TextAnchor('msg:1', 4));
    selection.extend(const TextAnchor('msg:1', 4));

    // begin always notifies; extend only on a change.
    expect(notes, 3);
  });

  group('ProjectMemory', () {
    test('the project selected last is remembered, per host and root', () {
      final store = <String, String>{};
      final memory = ProjectMemory(
        read: (key) => store[key],
        save: (key, value) => store[key] = value,
      );

      expect(memory.lastProject, isNull);
      memory.rememberProject('studio', '/home/u/alpha');
      expect(
        memory.lastProject,
        (host: 'studio', project: '/home/u/alpha'),
      );
    });

    test('the same project name on two hosts is kept apart', () {
      final store = <String, String>{};
      final memory = ProjectMemory(
        read: (key) => store[key],
        save: (key, value) => store[key] = value,
      );

      memory.rememberThread('studio', '/home/u/marshal', 'a-1');
      expect(memory.threadFor('studio', '/home/u/marshal'), 'a-1');
      // A name is not an identity: the same root on another host keeps its
      // own keys, so one host's thread never answers for the other's.
      expect(memory.threadFor('local', '/home/u/marshal'), isNull);
      memory.rememberProject('local', '/home/u/marshal');
      expect(memory.lastProject!.host, 'local');
      // Forgetting one host's thread does not touch the other's.
      memory.forgetThread('local', '/home/u/marshal');
      expect(memory.threadFor('local', '/home/u/marshal'), isNull);
      expect(memory.threadFor('studio', '/home/u/marshal'), 'a-1');
    });

    test('a corrupt preference names no project, and nothing throws', () {
      final store = <String, String>{};
      final memory = ProjectMemory(
        read: (key) => store[key],
        save: (key, value) => store[key] = value,
      );

      for (final kept in const [
        'no-slash',
        '/missing-host',
        'studio/missing-project/',
        'studio/%zz',
      ]) {
        store['project.last'] = kept;
        expect(memory.lastProject, isNull, reason: kept);
      }
      // What a selection writes is what a reload reads back.
      store['project.last'] = 'studio/%2Fhome%2Fu%2Falpha';
      expect(
        memory.lastProject,
        (host: 'studio', project: '/home/u/alpha'),
      );
    });

    test('the thread a project last had open is remembered and dropped', () {
      final store = <String, String>{};
      final memory = ProjectMemory(
        read: (key) => store[key],
        save: (key, value) => store[key] = value,
      );

      expect(memory.threadFor('local', '/home/u/alpha'), isNull);
      memory.rememberThread('local', '/home/u/alpha', 'a-9');
      expect(memory.threadFor('local', '/home/u/alpha'), 'a-9');
      expect(memory.threadFor('local', '/home/u/beta'), isNull);
      memory.forgetThread('local', '/home/u/alpha');
      expect(memory.threadFor('local', '/home/u/alpha'), isNull);
      expect(memory.threadFor('local', '/home/u/beta'), isNull);
    });
  });
}
