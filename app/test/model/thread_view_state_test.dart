import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/thread_view_state.dart';

void main() {
  test('density cycles outline, comfortable, full, outline', () {
    final view = ThreadViewState()..density = Density.outline;

    view.cycleDensity();
    expect(view.density, Density.comfortable);
    view.cycleDensity();
    expect(view.density, Density.full);
    view.cycleDensity();
    expect(view.density, Density.outline);
    view.dispose();
  });

  test('the cursor moves and is remembered', () {
    final view = ThreadViewState();
    expect(
      view.cursor,
      isNull,
      reason: 'nothing is cursored before the user moves',
    );

    view.cursor = 'step:1';
    expect(view.cursor, 'step:1');
    view.cursor = 'step:2';
    expect(view.cursor, 'step:2');
    view.dispose();
  });

  test('following starts on and can be turned off and on', () {
    final view = ThreadViewState();
    expect(view.following, isTrue);

    view.following = false;
    expect(view.following, isFalse);
    view.following = true;
    expect(view.following, isTrue);
    view.dispose();
  });

  test('a fold toggles and is remembered by node id', () {
    final view = ThreadViewState();
    expect(view.folded('turn:1'), isFalse);

    view.toggleFold('turn:1');
    expect(view.folded('turn:1'), isTrue);
    expect(view.folded('turn:2'), isFalse, reason: 'a fold is per node');

    view.toggleFold('turn:1');
    expect(view.folded('turn:1'), isFalse);
    view.dispose();
  });

  test('a draft is kept per thread', () {
    final view = ThreadViewState();
    expect(view.draft, isEmpty);

    view.draft = 'use the existing session store';
    expect(view.draft, 'use the existing session store');
    view.dispose();
  });

  test('a change notifies once, and an unchanged value does not', () {
    final view = ThreadViewState();
    var notifications = 0;
    view.addListener(() => notifications++);

    view.draft = 'hello';
    view.draft = 'hello';
    view.following = false;
    view.following = false;
    view.toggleFold('step:1');
    view.cursor = 'step:1';
    view.cursor = 'step:1';

    expect(notifications, 4);
    view.dispose();
  });
}
