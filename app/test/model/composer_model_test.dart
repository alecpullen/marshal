import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/composer_model.dart';
import 'package:marshal_app/src/model/thread_view_state.dart';

/// The routes a composer posts, recorded so a test can see which one ran.
class _Routes {
  final prompts = <String>[];
  final steers = <String>[];
  final modes = <String>[];
  int cancels = 0;
  String accessMode = 'act';
  bool fail = false;

  Future<void> prompt(String text) async {
    if (fail) throw Exception('refused');
    prompts.add(text);
  }

  Future<void> steer(String text) async {
    if (fail) throw Exception('refused');
    steers.add(text);
  }

  Future<void> cancel() async {
    cancels++;
  }

  Future<void> setMode(String mode) async {
    if (fail) throw Exception('refused');
    modes.add(mode);
    accessMode = mode;
  }

  Future<AccessState> access() async =>
      AccessState(mode: accessMode, auto: false, system: false, revision: 1);

  ComposerModel model(ThreadViewState view, {bool running = false}) =>
      ComposerModel(
        prompt: prompt,
        steer: steer,
        cancel: cancel,
        setMode: setMode,
        access: access,
        view: view,
        running: running,
      );
}

void main() {
  late ThreadViewState view;
  late _Routes routes;

  setUp(() {
    view = ThreadViewState();
    routes = _Routes();
  });

  tearDown(() => view.dispose());

  test('prompts when the session is idle', () async {
    final model = routes.model(view);
    await model.send('fix the login bug');

    expect(routes.prompts, ['fix the login bug']);
    expect(routes.steers, isEmpty);
    expect(model.running, isTrue, reason: 'a prompt starts the turn');
    expect(view.draft, isEmpty, reason: 'the draft clears once accepted');
    model.dispose();
  });

  test('steers when the turn is running', () async {
    final model = routes.model(view, running: true);
    await model.send('use the existing store');

    expect(routes.steers, ['use the existing store']);
    expect(routes.prompts, isEmpty);
    model.dispose();
  });

  test('an empty draft sends nothing', () async {
    final model = routes.model(view);
    await model.send('   ');
    expect(routes.prompts, isEmpty);
    model.dispose();
  });

  test('a refused send keeps the draft and says why', () async {
    final model = routes.model(view);
    view.draft = 'fix it';
    routes.fail = true;

    await model.send('fix it');

    expect(view.draft, 'fix it');
    expect(model.failure, contains('refused'));
    expect(model.running, isFalse);
    model.dispose();
  });

  test('stop cancels the turn', () async {
    final model = routes.model(view, running: true);
    await model.stop();

    expect(routes.cancels, 1);
    expect(model.running, isFalse);
    model.dispose();
  });

  test('the mode chip posts the mode it chose', () async {
    final model = routes.model(view);
    await model.selectMode('act');

    expect(routes.modes, ['act']);
    expect(model.permissionMode, 'Act');
    expect(model.readOnly, isFalse);
    model.dispose();
  });

  test('a session in ask mode is read-only, with its reason', () async {
    routes.accessMode = 'ask';
    final model = routes.model(view);
    await model.refreshAccess();

    expect(model.readOnly, isTrue);
    expect(model.readOnlyReason, contains('ask'));
    // A read-only composer sends nothing.
    await model.send('hello');
    expect(routes.prompts, isEmpty);
    model.dispose();
  });

  test('a draft belongs to the thread and outlives a composer', () {
    final first = routes.model(view);
    view.draft = 'half a thought';
    first.dispose();

    // A reconnect builds a new composer over the same thread's state.
    final second = routes.model(view);
    expect(view.draft, 'half a thought');
    second.dispose();
  });
}
