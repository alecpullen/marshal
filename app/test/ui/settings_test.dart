import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart' show DaemonInfo;
import 'package:marshal_app/src/client/fleet_profiles.dart';
import 'package:marshal_app/src/client/host.dart' show HostKind, HostReach;
import 'package:marshal_app/src/model/fleet_store.dart'
    show HostTunnel, HostView;
import 'package:marshal_app/src/model/general_model.dart';
import 'package:marshal_app/src/model/hosts_model.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/settings/general_section.dart';
import 'package:marshal_app/src/ui/settings/hosts_section.dart';
import 'package:marshal_app/src/ui/settings/settings_place.dart';
import 'package:marshal_app/src/ui/thread/thread_screen.dart' show threadKeys;

/// The `marshal fleet` commands as a test scripts them: every one is recorded,
/// in the order it ran, and none runs anything real.
class _Runner implements FleetProfiles {
  _Runner({this.failAdd});

  /// The words `add` refuses with, when the test asks for a failure.
  final String? failAdd;

  @override
  bool get supported => true;

  final calls = <String>[];

  @override
  Future<void> add(String name, String destination) async {
    calls.add('add $name $destination');
    final words = failAdd;
    if (words != null) throw FleetProfileFailure(words);
  }

  @override
  Future<void> remove(String name) async {
    calls.add('remove $name');
  }
}

/// Settings' frame, its Hosts section and its General section (F7 decision
/// 7): a host that is away marked with when it was last seen, a host added
/// where the build can add one, and appearance taking effect.
void main() {
  final now = DateTime.utc(2026, 11, 17, 4, 12);

  final hosts = [
    HostView(
      name: 'local',
      kind: HostKind.local,
      reach: HostReach.live,
      lastContact: now,
    ),
    HostView(
      name: 'studio',
      kind: HostKind.server,
      reach: HostReach.unreachable,
      lastContact: now.subtract(const Duration(minutes: 4)),
    ),
  ];

  final daemon = DaemonInfo(
    version: '1.4.0',
    profile: 'local',
    pid: 4821,
    startedAt: now
        .subtract(const Duration(hours: 3, minutes: 12))
        .millisecondsSinceEpoch,
    socket: '/home/u/.local/state/marshal/daemon.sock',
    projects: const ['/home/u/marshal'],
    pinned: const ['/home/u/widgets'],
    children: const [],
    hosts: const [],
  );

  /// The place with both sections this step has, on [section].
  Widget place({
    int section = 0,
    GeneralPreferences? preferences,
    StateKind? state,
    List<HostView>? listing,
    FleetProfiles? profiles,
    List<String>? stored,
    List<String>? cleared,
    List<String>? retried,
  }) {
    final prefs = preferences ?? GeneralPreferences();
    return SettingsPlace(
      preferences: prefs,
      index: section,
      state: state,
      sections: {
        SettingsSectionKind.hosts: (state) => HostsSection(
          model: HostsModel.fixture(
            listing ?? hosts,
            profiles: profiles,
            storeToken: stored == null
                ? null
                : (host, token) async => stored.add('$host: $token'),
            clearToken: cleared?.add,
            retry: retried?.add,
          ),
          state: state,
          now: now,
        ),
        SettingsSectionKind.general: (state) => GeneralSection(
          model: GeneralModel.fixture(
            preferences: prefs,
            keyboard: [
              for (final key in threadKeys) (key.key, key.description),
            ],
            daemon: daemon,
            now: () => now,
          ),
          state: state,
        ),
      },
    );
  }

  Future<void> pump(
    WidgetTester tester,
    Widget screen, {
    Brightness? brightness = Brightness.light,
  }) async {
    tester.view.physicalSize = const Size(1200, 1000);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(EmberApp(brightness: brightness, home: screen));
  }

  testWidgets('an unreachable host shows when it was last seen', (
    tester,
  ) async {
    await pump(tester, place());

    expect(find.text('unreachable 4m'), findsOneWidget);
    expect(find.text('live'), findsOneWidget);
  });

  testWidgets('the add row is planned in a browser', (tester) async {
    await pump(tester, place());

    final planned = tester.widgetList<StateMessage>(find.byType(StateMessage));
    expect(
      planned.where(
        (message) =>
            message.kind == StateKind.planned &&
            message.message == 'Add a host',
      ),
      hasLength(1),
    );
  });

  testWidgets('the add row is real where hosts can be added', (tester) async {
    await pump(tester, place(profiles: _Runner()));

    await tester.tap(find.text('Add a host'));
    await tester.pumpAndSettle();

    expect(find.text('Name'), findsOneWidget);
    expect(find.text('SSH destination'), findsOneWidget);
    expect(
      find.text('URL'),
      findsNothing,
      reason: 'a host is reached over SSH, not over a URL',
    );
  });

  testWidgets('a failed add shows its words and keeps the fields', (
    tester,
  ) async {
    final runner = _Runner(failAdd: 'box is already in fleet.toml');
    await pump(tester, place(profiles: runner));

    await tester.tap(find.text('Add a host'));
    await tester.pumpAndSettle();
    await tester.enterText(find.byType(EditableText).at(0), 'box');
    await tester.enterText(find.byType(EditableText).at(1), 'me@box');
    await tester.tap(find.widgetWithText(Btn, 'Add'));
    await tester.pumpAndSettle();

    // The runner's words sit under the fields, and the fields keep what was
    // typed, so the name can be corrected rather than retyped.
    expect(find.text('box is already in fleet.toml'), findsOneWidget);
    expect(
      tester
          .widget<EditableText>(find.byType(EditableText).at(0))
          .controller
          .text,
      'box',
    );
    expect(
      tester
          .widget<EditableText>(find.byType(EditableText).at(1))
          .controller
          .text,
      'me@box',
    );
    expect(runner.calls, ['add box me@box']);
  });

  testWidgets('the local daemon cannot be removed', (tester) async {
    final runner = _Runner();
    await pump(tester, place(profiles: runner));

    await tester.tap(find.text('local'));
    await tester.pumpAndSettle();

    expect(find.text('Remove…'), findsNothing);
    expect(find.text('Sign in'), findsNothing);
    expect(runner.calls, isEmpty);
  });

  testWidgets('remove asks first', (tester) async {
    final runner = _Runner();
    await pump(
      tester,
      place(
        profiles: runner,
        listing: [
          hosts.first,
          HostView(
            name: 'studio',
            kind: HostKind.server,
            reach: HostReach.unreachable,
            lastContact: now.subtract(const Duration(minutes: 4)),
            destination: 'me@studio',
          ),
        ],
      ),
    );

    await tester.tap(find.text('studio'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Remove…'));
    await tester.pumpAndSettle();

    // Nothing is removed until the confirmation says what is lost.
    expect(runner.calls, isEmpty);
    expect(find.textContaining('stops showing it'), findsOneWidget);
    expect(find.textContaining('Its daemon keeps running'), findsOneWidget);

    await tester.tap(find.widgetWithText(Btn, 'Remove'));
    await tester.pumpAndSettle();

    expect(runner.calls, ['remove studio']);
  });

  testWidgets('an SSH host shows its destination, tunnel and last error', (
    tester,
  ) async {
    final since = now.subtract(const Duration(minutes: 4));
    await pump(
      tester,
      place(
        profiles: _Runner(),
        listing: [
          hosts.first,
          HostView(
            name: 'box',
            kind: HostKind.server,
            reach: HostReach.unreachable,
            lastContact: since,
            destination: 'me@box',
            tunnel: HostTunnel(
              state: 'down',
              lastError: 'Permission denied (publickey).',
              since: since,
            ),
          ),
        ],
      ),
    );

    await tester.tap(find.text('box'));
    await tester.pumpAndSettle();

    final detail = find.byType(HostDetail);
    expect(
      find.descendant(of: detail, matching: find.text('Over SSH to me@box')),
      findsOneWidget,
    );
    expect(
      find.descendant(of: detail, matching: find.text('me@box')),
      findsOneWidget,
      reason: 'the SSH fact names the destination',
    );
    expect(
      find.descendant(of: detail, matching: find.text('down since 4m ago')),
      findsOneWidget,
    );
    expect(
      find.descendant(
        of: detail,
        matching: find.text('Permission denied (publickey).'),
      ),
      findsOneWidget,
    );
  });

  testWidgets('an unauthorized host offers sign in', (tester) async {
    await pump(
      tester,
      place(
        profiles: _Runner(),
        stored: <String>[],
        listing: [
          hosts.first,
          HostView(
            name: 'studio',
            kind: HostKind.server,
            reach: HostReach.unauthorized,
            lastContact: now.subtract(const Duration(minutes: 4)),
          ),
        ],
      ),
    );

    await tester.tap(find.text('studio'));
    await tester.pumpAndSettle();

    expect(find.text('Sign in'), findsOneWidget);
    expect(find.text('Token'), findsOneWidget);
  });

  testWidgets('a native build offers no sign in', (tester) async {
    // A native build keeps no token of its own: the daemon reaches its hosts
    // over the tunnels it keeps, so there is nothing here to sign in to.
    await pump(
      tester,
      place(
        profiles: _Runner(),
        listing: [
          hosts.first,
          HostView(
            name: 'studio',
            kind: HostKind.server,
            reach: HostReach.unauthorized,
            lastContact: now.subtract(const Duration(minutes: 4)),
          ),
        ],
      ),
    );

    await tester.tap(find.text('studio'));
    await tester.pumpAndSettle();

    expect(find.text('Sign in'), findsNothing);
    expect(find.text('Token'), findsNothing);
  });

  testWidgets('a relayed host says where it is reached', (tester) async {
    await pump(
      tester,
      place(
        listing: [
          hosts.first,
          HostView(
            name: 'hub',
            kind: HostKind.server,
            reach: HostReach.live,
            lastContact: now,
          ),
          HostView(
            name: 'laptop',
            kind: HostKind.server,
            reach: HostReach.live,
            lastContact: now,
            via: 'hub',
          ),
        ],
      ),
    );

    await tester.tap(find.text('laptop'));
    await tester.pumpAndSettle();

    expect(
      find.descendant(
        of: find.byType(HostDetail),
        matching: find.text('A server profile, through hub'),
      ),
      findsOneWidget,
    );
  });

  testWidgets('a relayed host behind a refused relay says to sign in to the '
      'relay', (tester) async {
    await pump(
      tester,
      place(
        profiles: _Runner(),
        listing: [
          hosts.first,
          HostView(
            name: 'hub',
            kind: HostKind.server,
            reach: HostReach.unauthorized,
            lastContact: now.subtract(const Duration(minutes: 4)),
          ),
          HostView(
            name: 'laptop',
            kind: HostKind.server,
            reach: HostReach.unauthorized,
            lastContact: now.subtract(const Duration(minutes: 4)),
            via: 'hub',
          ),
        ],
      ),
    );

    await tester.tap(find.text('laptop'));
    await tester.pumpAndSettle();

    expect(find.text('Sign in to the relay first.'), findsOneWidget);
    expect(
      find.text('Sign in'),
      findsNothing,
      reason: 'signing in to the host cannot clear the relay’s refusal',
    );
  });

  testWidgets('a relayed host offers sign out where a store can clear it', (
    tester,
  ) async {
    final stored = <String>[];
    final cleared = <String>[];
    final retried = <String>[];
    await pump(
      tester,
      place(
        profiles: _Runner(),
        stored: stored,
        cleared: cleared,
        retried: retried,
        listing: [
          hosts.first,
          HostView(
            name: 'hub',
            kind: HostKind.server,
            reach: HostReach.live,
            lastContact: now,
          ),
          HostView(
            name: 'laptop',
            kind: HostKind.server,
            reach: HostReach.unauthorized,
            lastContact: now.subtract(const Duration(minutes: 4)),
            via: 'hub',
          ),
        ],
      ),
    );

    await tester.tap(find.text('laptop'));
    await tester.pumpAndSettle();

    // A browser signs in by writing the host's own key, and then asks the host
    // to try again (decision 6).
    await tester.enterText(
      find.descendant(
        of: find.byType(Field),
        matching: find.byType(EditableText),
      ),
      'a-token',
    );
    await tester.tap(find.text('Sign in'));
    await tester.pumpAndSettle();

    expect(stored, ['laptop: a-token']);
    expect(retried, ['laptop']);

    // Signing out clears that key and asks again, so the host reads "token
    // refused" until it is signed in to once more (spec D22).
    await tester.tap(find.text('Sign out'));
    await tester.pumpAndSettle();

    expect(cleared, ['laptop']);
    expect(retried, ['laptop', 'laptop']);
  });

  testWidgets('appearance changes the theme', (tester) async {
    final preferences = GeneralPreferences();
    await pump(tester, place(section: 9, preferences: preferences));

    // General opens as the system's.
    expect(preferences.appearance, Appearance.system);

    await tester.tap(find.text('Appearance'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Dark'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Save'));
    await tester.pumpAndSettle();

    // The choice is the window's, not the place's: the root holds it (the
    // whole window is under it), so what this place leaves behind is the
    // preference itself.
    expect(preferences.appearance, Appearance.dark);
  });

  testWidgets('Settings follows the window, not the system', (tester) async {
    // Finding 3: the window chose dark, the machine is in light. The place
    // draws no theme of its own, so its text is the window's dark `fg` —
    // not the system's light `fg`, which was near-invisible on a dark
    // background.
    tester.platformDispatcher.platformBrightnessTestValue = Brightness.light;
    addTearDown(tester.platformDispatcher.clearPlatformBrightnessTestValue);

    await pump(tester, place(), brightness: Brightness.dark);

    // The open section's own title is the place's heading.
    final heading = tester.widget<Text>(find.text('Hosts'));
    expect(heading.style?.color, Ember.dark.fg);
    expect(heading.style?.color, isNot(Ember.light.fg));
  });
}
