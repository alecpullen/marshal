import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart' show DaemonInfo;
import 'package:marshal_app/src/client/fleet_profiles.dart';
import 'package:marshal_app/src/client/host.dart' show HostKind, HostReach;
import 'package:marshal_app/src/model/fleet_store.dart' show HostView;
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
  @override
  bool get supported => true;

  final calls = <String>[];

  @override
  Future<void> add(String name, Uri url, {String? caFile}) async {
    calls.add('add $name');
  }

  @override
  Future<void> login(String name, String token) async {
    calls.add('login $name');
  }

  @override
  Future<void> remove(String name) async {
    calls.add('remove $name');
  }
}

/// Settings' frame, its Hosts section and its General section (F7 decision
/// 7): the ten sections in the design's order, a host that is away marked
/// with when it was last seen, a host added where the build can add one, and
/// appearance taking effect.
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
  );

  /// The place with both sections this step has, on [section].
  Widget place({
    int section = 0,
    GeneralPreferences? preferences,
    StateKind? state,
    List<HostView>? listing,
    FleetProfiles? profiles,
  }) {
    final prefs = preferences ?? GeneralPreferences();
    return SettingsPlace(
      preferences: prefs,
      index: section,
      state: state,
      sections: {
        SettingsSectionKind.hosts: (state) => HostsSection(
          model: HostsModel.fixture(listing ?? hosts, profiles: profiles),
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

  Future<void> pump(WidgetTester tester, Widget screen) async {
    tester.view.physicalSize = const Size(1200, 1000);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: screen),
    );
  }

  testWidgets('sections in the specified order', (tester) async {
    await pump(tester, place());

    final labels = [
      for (final row in tester.widgetList<NavRow>(find.byType(NavRow)))
        row.label,
    ];
    expect(labels, [
      'Hosts',
      'Environments',
      'Connections',
      'Models and routing',
      'Secrets',
      'Network',
      'Usage and audit',
      'Notifications',
      'Access',
      'General',
    ]);
  });

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
    expect(find.text('URL'), findsOneWidget);
    expect(find.text('CA file'), findsOneWidget);
    expect(find.text('Token'), findsOneWidget);
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
    await pump(tester, place(profiles: runner));

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

  testWidgets('an unauthorized host offers sign in', (tester) async {
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

    expect(find.text('Sign in'), findsOneWidget);
    expect(find.text('Token'), findsOneWidget);
  });

  testWidgets('appearance changes the theme', (tester) async {
    final preferences = GeneralPreferences();
    await pump(tester, place(section: 9, preferences: preferences));

    // General opens as the system's, and the place holds no brightness of
    // its own until one is chosen.
    expect(preferences.appearance, Appearance.system);
    expect(
      find.byWidgetPredicate(
        (w) => w is EmberTheme && w.brightness == Brightness.dark,
      ),
      findsNothing,
    );

    await tester.tap(find.text('Appearance'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Dark'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Save'));
    await tester.pumpAndSettle();

    expect(preferences.appearance, Appearance.dark);
    expect(
      find.byWidgetPredicate(
        (w) => w is EmberTheme && w.brightness == Brightness.dark,
      ),
      findsOneWidget,
    );
  });
}
