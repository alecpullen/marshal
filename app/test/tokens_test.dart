import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/tokens.dart';

/// design-system.md §2: light and dark follow the system unless the user
/// chooses one.
void main() {
  Future<Ember> resolveUnder(
    WidgetTester tester, {
    required Brightness platform,
    Brightness? chosen,
  }) async {
    late Ember seen;
    await tester.pumpWidget(
      MediaQuery(
        data: MediaQueryData(platformBrightness: platform),
        child: EmberTheme(
          brightness: chosen,
          child: Builder(
            builder: (context) {
              seen = Ember.of(context);
              return const SizedBox();
            },
          ),
        ),
      ),
    );
    return seen;
  }

  testWidgets('follows the system when nothing is chosen', (tester) async {
    expect(await resolveUnder(tester, platform: Brightness.dark), Ember.dark);
    expect(await resolveUnder(tester, platform: Brightness.light), Ember.light);
  });

  testWidgets('a choice wins over the system', (tester) async {
    expect(
      await resolveUnder(
        tester,
        platform: Brightness.dark,
        chosen: Brightness.light,
      ),
      Ember.light,
    );
    expect(
      await resolveUnder(
        tester,
        platform: Brightness.light,
        chosen: Brightness.dark,
      ),
      Ember.dark,
    );
  });

  test('every role is in one of the two bundled families', () {
    for (final style in Ember.light.roles.values) {
      expect([Ember.sans, Ember.monoFamily], contains(style.fontFamily));
    }
    // Numbers that change or line up are mono (§3).
    expect(Ember.light.mono.fontFamily, Ember.monoFamily);
    expect(Ember.light.monoSmall.fontFamily, Ember.monoFamily);
  });
}
