import 'package:flutter/foundation.dart';

import 'place.dart';

/// The line a link from the old Studio shows when this build has no place for
/// it (F8 decision 2).
const outOfDateLink = 'That link is out of date, so this opened the Overview.';

/// What the page a person opened names, and the one line to show when the
/// link was written for a Studio this build no longer has.
@immutable
class StartPlace {
  const StartPlace(this.place, {this.notice, this.fromHash = false});

  final Place place;

  /// What to say above the place, when the link named nothing this build
  /// knows. Null when the link is current.
  final String? notice;

  /// The page named the place in a hash written for the Svelte Studio, so the
  /// address is replaced with this build's path for it (F8 decision 2).
  final bool fromHash;

  @override
  bool operator ==(Object other) =>
      other is StartPlace &&
      other.place == place &&
      other.notice == notice &&
      other.fromHash == fromHash;

  @override
  int get hashCode => Object.hash(place, notice, fromHash);

  @override
  String toString() =>
      'StartPlace($place${notice == null ? '' : ', $notice'}'
      '${fromHash ? ', fromHash' : ''})';
}

/// The place the page a person opened shows (F8 decision 2).
///
/// [hash] is the fragment the page carried, `#` included: the whole route a
/// link written for the one-bridge Studio named. [path] is the part under the
/// Studio's base, which is what this build's own links carry, so a reload and
/// Back work.
///
/// A hash that names no place this build has opens the Overview with one line
/// saying the link is out of date ([outOfDateLink]); a path is read as it
/// stands.
StartPlace startPlace({required String path, String hash = ''}) {
  if (hash.isNotEmpty) {
    // Flutter web's own hash strategy writes this build's path as `#/…`; that
    // is a place of this build, not a route the Svelte Studio ever wrote.
    if (hash.startsWith('#/')) {
      return StartPlace(parsePlace(hash.substring(1)));
    }
    final place = translateLegacyHash(hash);
    if (place == null) {
      return const StartPlace(
        Place.overview(),
        notice: outOfDateLink,
        fromHash: true,
      );
    }
    return StartPlace(place, fromHash: true);
  }
  return StartPlace(parsePlace(path));
}

/// The new place for a Svelte hash route, or null when it is unknown.
///
/// Every route `web/ui/src/lib/routes.ts` parsed has a row here (F8 decision
/// 2), including the redirects an older page left behind (`#clients`,
/// `#disk`, `#activity`). The order is that file's `pageFromHash`, because its
/// parsers overlap: `#factory/drafts` is a draft, not an item called
/// "drafts".
///
/// A route the app has no single place for goes to the nearest place a person
/// can carry on from: the new-work dialog's routes open the project they name
/// (or the Overview), and the roadmap's areas open the section whose planned
/// row they describe.
Place? translateLegacyHash(String hash) {
  final route = hash.trim();
  // The empty hash, `#` and `#fleet` are the fleet itself.
  if (route.isEmpty || route == '#' || route == '#fleet') {
    return const Place.overview();
  }
  if (route == '#inbox') return const Place(PlaceKind.inbox);

  // The new-work dialog: `#new[?project=<root>]`.
  if (_newAgent.hasMatch(route)) return _scoped(_queryValue(route, 'project'));
  if (_plannerNew.hasMatch(route)) {
    return _scoped(_queryValue(route, 'project'));
  }

  final planner = _planner.firstMatch(route);
  if (planner != null) return Place(PlaceKind.thread, id: planner.group(1)!);

  final factory = _factory(route);
  if (factory != null) return factory;

  // `#chat/<id>[/review][?...]`: the thread's own place.
  final chat = _chat.firstMatch(route);
  if (chat != null) return Place(PlaceKind.thread, id: _decode(chat.group(1)!));

  // `#sessions` is the fleet's; `#sessions/<root>` scopes it to one project.
  if (route == '#sessions') return const Place.overview();
  final sessions = _sessions.firstMatch(route);
  if (sessions != null) {
    return Place(PlaceKind.project, id: _decode(sessions.group(1)!));
  }

  // `#runs/<id>` is the run's thread: a run is an agent's session.
  final run = _run.firstMatch(route);
  if (run != null) return Place(PlaceKind.thread, id: _decode(run.group(1)!));
  if (route == '#runs') return const Place.overview();

  if (_live.hasMatch(route)) return const Place.overview();

  final library = _library.firstMatch(route);
  if (library != null) return _libraryPlace(library.group(1));

  final settings = _settings.firstMatch(route);
  if (settings != null) return _settingsPlace(settings.group(1));

  if (_usage.hasMatch(route)) return _section(PlaceKind.settings, 'usage');

  // `#watches` and `#watches/schedules`.
  if (route == '#watches') return _section(PlaceKind.automations, 'watches');
  if (route == '#watches/schedules') {
    return _section(PlaceKind.automations, 'schedules');
  }

  final workspaceNetwork = _workspaceNetwork.firstMatch(route);
  if (workspaceNetwork != null) return _section(PlaceKind.settings, 'network');
  if (_network.hasMatch(route)) return _section(PlaceKind.settings, 'network');

  final project = _project.firstMatch(route);
  if (project != null) {
    // `#projects/<root>/automations/<page>` is the project's bots.
    if (project.group(2) != null) {
      return _section(PlaceKind.automations, 'bots');
    }
    return Place(PlaceKind.project, id: _decode(project.group(1)!));
  }

  final workspaces = _workspaces.firstMatch(route);
  if (workspaces != null) return _section(PlaceKind.settings, 'environments');
  if (_workspacesGallery.hasMatch(route)) {
    return _section(PlaceKind.settings, 'environments');
  }

  final roadmap = _roadmap.firstMatch(route);
  if (roadmap != null) return _roadmapPlace(roadmap.group(1)!);

  // Where an older page's own route moved to (`redirectLegacy`).
  switch (route) {
    case '#clients':
      return _section(PlaceKind.library, 'mcp');
    case '#disk':
    case '#activity':
      return _section(PlaceKind.settings, 'usage');
  }
  return null;
}

/// A place with one of its sections open.
Place _section(PlaceKind kind, String section) => Place(kind, section: section);

/// The `#new`-family routes: the project they name, or the Overview, which is
/// where a person starts work when the link names no project.
Place _scoped(String? projectRoot) {
  final root = projectRoot == null ? '' : _decode(projectRoot);
  return root.isEmpty
      ? const Place.overview()
      : Place(PlaceKind.project, id: root);
}

/// The factory's hashes: the global list, a project's list, the working
/// drafts, creation, and one item.
///
/// The generic item form is matched last, because it would otherwise read
/// `#factory/drafts` as an item called "drafts" — `routes.ts` orders them the
/// same way.
Place? _factory(String route) {
  if (_factoryGlobal.hasMatch(route)) return const Place.overview();

  final scoped = _factoryScoped.firstMatch(route);
  if (scoped != null) {
    return Place(PlaceKind.project, id: _decode(scoped.group(1)!));
  }

  // The working drafts, `#factory/drafts[/new|/<id>][?project=<root>]`: a
  // draft belongs to the project its query names, and the app shows them on
  // that project's page.
  if (_factoryDrafts.hasMatch(route)) {
    return _scoped(_queryValue(route, 'project'));
  }

  if (_factoryNew.hasMatch(route)) {
    return _scoped(_queryValue(route, 'project'));
  }

  final item = _factoryItem.firstMatch(route);
  if (item != null) return Place(PlaceKind.thread, id: _decode(item.group(1)!));
  return null;
}

/// Which section of Library [tab] names: `recipes` moved to Automations, and
/// an unknown tab is not a Library route at all.
Place? _libraryPlace(String? tab) {
  final name = tab ?? 'skills';
  if (!librarySections.contains(name)) {
    // `recipes` is a Library tab in the Svelte Studio and an Automations
    // section here (F7 decision 4).
    return name == 'recipes'
        ? _section(PlaceKind.automations, 'recipes')
        : null;
  }
  return _section(PlaceKind.library, name);
}

/// Which section of Settings a Svelte tab names.
///
/// The two Studios group these differently: the Svelte Studio's Providers are
/// part of Models and routing here, its Credentials are Secrets, its Repos are
/// Connections, and its Tokens and Status links are Access.
Place? _settingsPlace(String? tab) {
  final name = tab ?? 'models';
  return switch (name) {
    'models' || 'providers' => _section(PlaceKind.settings, 'models'),
    'secrets' || 'credentials' => _section(PlaceKind.settings, 'secrets'),
    'repos' => _section(PlaceKind.settings, 'connections'),
    'tokens' || 'status-links' => _section(PlaceKind.settings, 'access'),
    'notifications' => _section(PlaceKind.settings, 'notifications'),
    _ => null,
  };
}

/// The place a roadmap area's planned row is described in: the three areas the
/// Svelte navigation linked are Settings sections, the planning areas have no
/// section yet and open the Overview, and anything else was never a route.
Place? _roadmapPlace(String area) => switch (area) {
  'hosts' => _section(PlaceKind.settings, 'hosts'),
  'environments' => _section(PlaceKind.settings, 'environments'),
  'connections' => _section(PlaceKind.settings, 'connections'),
  'planning' || 'voice' || 'workflow' => const Place.overview(),
  _ => null,
};

/// The value of [key] in the hash's query, still escaped.
String? _queryValue(String route, String key) {
  final start = route.indexOf('?');
  if (start < 0) return null;
  final end = route.indexOf('#', start);
  try {
    final params = Uri.splitQueryString(
      route.substring(start + 1, end < 0 ? route.length : end),
    );
    final value = params[key];
    return value == null || value.isEmpty ? null : value;
  } on FormatException {
    return null;
  } on ArgumentError {
    return null;
  }
}

/// [value] decoded, or as it stands when the escape is malformed: the Svelte
/// parsers degrade the same way rather than dropping the route.
String _decode(String value) {
  try {
    return Uri.decodeComponent(value);
  } on ArgumentError {
    return value;
  }
}

// The routes, in `routes.ts`'s order and with its own patterns.

final _newAgent = RegExp(r'^#new(?:\?(.*))?$');
// A planner id is the 32-hex identity the bridge mints; anything else was
// never a route, in `routes.ts` either.
final _planner = RegExp(r'^#planner/([0-9a-f]{32})$');
final _plannerNew = RegExp(r'^#planner/new(?:\?.*)?$');
final _chat = RegExp(r'^#chat/([^/?]+)(?:/review)?(?:\?(.*))?$');
final _sessions = RegExp(r'^#sessions/(.+)$');
final _run = RegExp(r'^#runs/([^/?]+)(?:\?(.*))?$');
final _live = RegExp(r'^#live(?:\?(.*))?$');
final _library = RegExp(r'^#library(?:/([^/?]+))?(?:\?(.*))?$');
final _settings = RegExp(r'^#settings(?:/([^/?]+))?$');
final _usage = RegExp(r'^#usage(?:\?(.*))?$');
final _network = RegExp(r'^#network(?:\?(.*))?$');
final _workspaceNetwork = RegExp(r'^#workspaces/([^/?]+)/network$');
final _project = RegExp(
  r'^#projects/([^/?]+)(?:/automations/(review-bot|ci-fixer)(?:\?(.*))?)?$',
);
final _workspaces = RegExp(r'^#workspaces/([^/?]+)/(edit|builds|network)$');
final _workspacesGallery = RegExp(r'^#workspaces$');
final _roadmap = RegExp(r'^#roadmap/([^/?]+)$');

final _factoryGlobal = RegExp(r'^#factory(?:\?(.*))?$');
final _factoryScoped = RegExp(r'^#projects/([^/?]+)/factory(?:\?(.*))?$');
final _factoryDrafts = RegExp(
  r'^#factory/drafts(?:/new|/[^/?#]+)?(?:\?[^#]*)?$',
);
final _factoryNew = RegExp(r'^#factory/new(?:\?([^#]*))?$');
final _factoryItem = RegExp(r'^#factory/([^/?#]+)(?:\?(.*))?$');
