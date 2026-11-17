import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/route_translation.dart';

/// Every route the Svelte Studio parsed, and the place it opens now (F8
/// decision 2). The table is the whole of `web/ui/src/lib/routes.ts`: a route
/// missing here is an old bookmark that would open the Overview instead of
/// the place it names.
void main() {
  const alpha = '/w/alpha';
  const encodedAlpha = '%2Fw%2Falpha';
  const plannerId = '0123456789abcdef0123456789abcdef';

  /// `#projects/<root>/automations/<page>` and the other project-scoped forms.
  const cases = <(String, Place?)>[
    // The fleet itself: the empty hash, `#` and `#fleet`.
    ('', Place.overview()),
    ('#', Place.overview()),
    ('#fleet', Place.overview()),
    ('#inbox', Place(PlaceKind.inbox)),

    // The new-work dialog has no place of its own: it opens the project it
    // names, or the Overview.
    ('#new', Place.overview()),
    ('#new?project=$encodedAlpha', Place(PlaceKind.project, id: alpha)),

    // The planner: a planning thread, and its creation form.
    ('#planner/new', Place.overview()),
    ('#planner/new?project=$encodedAlpha', Place(PlaceKind.project, id: alpha)),
    ('#planner/$plannerId', Place(PlaceKind.thread, id: plannerId)),

    // The factory: the global list, a project's list, drafts, creation, and
    // one item.
    ('#factory', Place.overview()),
    ('#factory?filter=needs-you', Place.overview()),
    ('#projects/$encodedAlpha/factory', Place(PlaceKind.project, id: alpha)),
    // The project's list with a filter on it is still that project's list.
    (
      '#projects/$encodedAlpha/factory?filter=needs-you',
      Place(PlaceKind.project, id: alpha),
    ),
    ('#factory/drafts', Place.overview()),
    (
      '#factory/drafts?project=$encodedAlpha',
      Place(PlaceKind.project, id: alpha),
    ),
    (
      '#factory/drafts/new?project=$encodedAlpha',
      Place(PlaceKind.project, id: alpha),
    ),
    (
      '#factory/drafts/d-1?project=$encodedAlpha',
      Place(PlaceKind.project, id: alpha),
    ),
    ('#factory/new', Place.overview()),
    ('#factory/new?project=$encodedAlpha', Place(PlaceKind.project, id: alpha)),
    ('#factory/item-1', Place(PlaceKind.thread, id: 'item-1')),
    (
      '#factory/item-1?view=activity&agent=a-1',
      Place(PlaceKind.thread, id: 'item-1'),
    ),

    // A thread, in its session and its review view.
    ('#chat/a-1', Place(PlaceKind.thread, id: 'a-1')),
    ('#chat/a-1/review', Place(PlaceKind.thread, id: 'a-1')),
    ('#chat/a-1?tab=changes&dock=expanded', Place(PlaceKind.thread, id: 'a-1')),

    // The sessions panel: the fleet's, and one project's.
    ('#sessions', Place.overview()),
    ('#sessions/$encodedAlpha', Place(PlaceKind.project, id: alpha)),

    // A run is an agent's session, so its place is the thread's.
    ('#runs/a-1', Place(PlaceKind.thread, id: 'a-1')),
    ('#runs/a-1?view=timeline', Place(PlaceKind.thread, id: 'a-1')),
    ('#runs', Place.overview()),

    // The live wall.
    ('#live', Place.overview()),
    ('#live?project=$encodedAlpha&runs=1&page=2', Place.overview()),

    // Library. Recipes is an Automations section in this app (F7 decision 4).
    ('#library', Place(PlaceKind.library, section: 'skills')),
    // A project scope on the Library link does not change the tab it names.
    (
      '#library?project=$encodedAlpha',
      Place(PlaceKind.library, section: 'skills'),
    ),
    ('#library/skills', Place(PlaceKind.library, section: 'skills')),
    ('#library/plugins', Place(PlaceKind.library, section: 'plugins')),
    ('#library/mcp', Place(PlaceKind.library, section: 'mcp')),
    ('#library/memory', Place(PlaceKind.library, section: 'memory')),
    ('#library/recipes', Place(PlaceKind.automations, section: 'recipes')),
    // Not a Library tab in either Studio.
    ('#library/bogus', null),

    // Settings. The Svelte tabs group differently from this app's sections
    // (F7 decision 7), and the mapping is the whole of that difference.
    ('#settings', Place(PlaceKind.settings, section: 'models')),
    ('#settings/models', Place(PlaceKind.settings, section: 'models')),
    ('#settings/providers', Place(PlaceKind.settings, section: 'models')),
    ('#settings/secrets', Place(PlaceKind.settings, section: 'secrets')),
    ('#settings/credentials', Place(PlaceKind.settings, section: 'secrets')),
    ('#settings/repos', Place(PlaceKind.settings, section: 'connections')),
    ('#settings/tokens', Place(PlaceKind.settings, section: 'access')),
    ('#settings/status-links', Place(PlaceKind.settings, section: 'access')),
    (
      '#settings/notifications',
      Place(PlaceKind.settings, section: 'notifications'),
    ),
    ('#settings/bogus', null),

    // Usage and audit.
    ('#usage', Place(PlaceKind.settings, section: 'usage')),
    ('#usage?tab=disk', Place(PlaceKind.settings, section: 'usage')),

    // Watches: the list and its schedules.
    ('#watches', Place(PlaceKind.automations, section: 'watches')),
    ('#watches/schedules', Place(PlaceKind.automations, section: 'schedules')),

    // The network inspector is Settings' Network section.
    ('#network', Place(PlaceKind.settings, section: 'network')),
    ('#network?agent=a-1', Place(PlaceKind.settings, section: 'network')),
    (
      '#workspaces/go-service/network',
      Place(PlaceKind.settings, section: 'network'),
    ),

    // A project page, and its automations pages.
    ('#projects/$encodedAlpha', Place(PlaceKind.project, id: alpha)),
    (
      '#projects/$encodedAlpha/automations/review-bot',
      Place(PlaceKind.automations, section: 'bots'),
    ),
    (
      '#projects/$encodedAlpha/automations/ci-fixer?draft=d-1',
      Place(PlaceKind.automations, section: 'bots'),
    ),

    // Environments: the gallery and the designer's three views.
    ('#workspaces', Place(PlaceKind.settings, section: 'environments')),
    (
      '#workspaces/go-service/edit',
      Place(PlaceKind.settings, section: 'environments'),
    ),
    (
      '#workspaces/go-service/builds',
      Place(PlaceKind.settings, section: 'environments'),
    ),

    // The roadmap's areas are the Settings sections whose planned rows they
    // describe; the planning areas have no section yet.
    ('#roadmap/hosts', Place(PlaceKind.settings, section: 'hosts')),
    (
      '#roadmap/environments',
      Place(PlaceKind.settings, section: 'environments'),
    ),
    ('#roadmap/connections', Place(PlaceKind.settings, section: 'connections')),
    ('#roadmap/planning', Place.overview()),
    ('#roadmap/voice', Place.overview()),
    ('#roadmap/workflow', Place.overview()),
    ('#roadmap/bogus', null),

    // Where an older page's route moved to (`redirectLegacy`).
    ('#clients', Place(PlaceKind.library, section: 'mcp')),
    ('#disk', Place(PlaceKind.settings, section: 'usage')),
    ('#activity', Place(PlaceKind.settings, section: 'usage')),

    // Nothing parsed these either.
    ('#bogus', null),
    ('#settings/extra/still', null),
  ];

  test('every route in routes.ts opens the place it names', () {
    for (final (hash, expected) in cases) {
      expect(
        translateLegacyHash(hash),
        expected,
        reason: '$hash should open $expected',
      );
    }
  });

  test('an unknown hash opens the overview with a notice', () {
    final start = startPlace(path: '/', hash: '#bogus');

    expect(start.place, const Place.overview());
    expect(start.notice, outOfDateLink);
  });

  test('a known hash opens its place with no notice', () {
    final start = startPlace(path: '/', hash: '#settings/secrets');

    expect(start.place, const Place(PlaceKind.settings, section: 'secrets'));
    expect(start.notice, isNull);
  });

  test('the path is the place when the page carries no hash', () {
    expect(
      startPlace(path: '/thread/local/a-1'),
      const StartPlace(Place(PlaceKind.thread, host: 'local', id: 'a-1')),
    );
    expect(startPlace(path: '/inbox').place, const Place(PlaceKind.inbox));
    expect(startPlace(path: '/').place, const Place.overview());
    // An unknown path is still a link that cannot be honoured.
    expect(startPlace(path: '/bogus').notice, isNull);
  });

  test(
    "Flutter web's own hash form is this build's path, not a Svelte route",
    () {
      final start = startPlace(path: '/', hash: '#/library/memory');

      expect(start.place, const Place(PlaceKind.library, section: 'memory'));
      expect(start.notice, isNull);
      expect(start.fromHash, isFalse);
    },
  );
}
