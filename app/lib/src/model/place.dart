import 'package:flutter/foundation.dart';

/// The places the Studio has (F2 decision 4), and only these.
///
/// A place is where a person is, not what is drawn: two places may draw the
/// same widget for now, and a planned place draws the message that says which
/// phase brings it.
enum PlaceKind {
  overview,
  inbox,
  pulls,
  project,
  thread,
  automations,
  library,
  settings,
}

/// The query key a link carries the inspector's width under.
const linkWidthKey = 'w';

/// The query key a link carries the inspector's tab under.
const linkTabKey = 't';

/// One place, with the target it names: a project's id, or a thread's host
/// and id.
///
/// In the browser the place and its target are the URL path under the
/// studio's base (`/next/`), so a reload and Back work (F2 decision 4); the
/// same parser serves a deep link on the desktop, where there is no URL.
@immutable
class Place {
  const Place(
    this.kind, {
    this.host,
    this.id,
    this.section,
    this.query = const {},
  }) : assert(
         kind != PlaceKind.project || id != null,
         'a project place names its project',
       ),
       assert(
         kind != PlaceKind.thread || id != null,
         'a thread place names its thread',
       ),
       assert(
         section == null ||
             kind == PlaceKind.automations ||
             kind == PlaceKind.library ||
             kind == PlaceKind.settings,
         'only Automations, Library and Settings have sections',
       );

  /// The overview: the fleet at a glance.
  const Place.overview() : this(PlaceKind.overview);

  /// The inbox: every ask, oldest first.
  const Place.inbox() : this(PlaceKind.inbox);

  final PlaceKind kind;

  /// The host a thread belongs to. Every other place spans hosts.
  ///
  /// A thread a link names by its id alone has none: a link written for the
  /// one-bridge Studio could not know which host answers for it, so the fleet
  /// is asked instead (F8 decision 2).
  final String? host;

  /// The project's id (its root) or the thread's id.
  final String? id;

  /// The section of Automations, Library or Settings a link names, in the
  /// vocabulary those places use for it ([automationsSections] and its
  /// siblings). Null is the place's first section.
  final String? section;

  /// What a link carries beyond the place itself: the inspector's width
  /// ([linkWidthKey]) and tab ([linkTabKey]) (F4 decision 3). Only the keys
  /// this build honours are kept, so a query a person adds by hand does not
  /// make a link a different place.
  final Map<String, String> query;

  /// This place with [query] instead of its own.
  Place withQuery(Map<String, String> query) => Place(
    kind,
    host: host,
    id: id,
    section: section,
    query: Map<String, String>.unmodifiable(query),
  );

  @override
  bool operator ==(Object other) =>
      other is Place &&
      other.kind == kind &&
      other.host == host &&
      other.id == id &&
      other.section == section &&
      mapEquals(other.query, query);

  @override
  int get hashCode => Object.hash(
    kind,
    host,
    id,
    section,
    Object.hashAllUnordered(
      query.entries.map((entry) => Object.hash(entry.key, entry.value)),
    ),
  );

  @override
  String toString() =>
      'Place(${kind.name}${host == null ? '' : ', $host'}'
      '${id == null ? '' : ', $id'}'
      '${section == null ? '' : ', $section'}'
      '${query.isEmpty ? '' : ', $query'})';
}

/// Automations' section names, in tab order.
const automationsSections = ['bots', 'schedules', 'watches', 'recipes'];

/// Library's section names, in tab order.
const librarySections = ['skills', 'plugins', 'mcp', 'memory'];

/// Settings' section names, in the design's order (F7 decision 7), which is
/// also `SettingsSectionKind`'s.
const settingsSections = [
  'hosts',
  'environments',
  'connections',
  'models',
  'secrets',
  'network',
  'usage',
  'notifications',
  'access',
  'general',
];

/// The section names [kind] accepts, or null for a place with no sections.
List<String>? sectionsOf(PlaceKind kind) => switch (kind) {
  PlaceKind.automations => automationsSections,
  PlaceKind.library => librarySections,
  PlaceKind.settings => settingsSections,
  _ => null,
};

/// Which section of [place] a link names: `0` when it names none, or a
/// section this build does not know — the place opens, at its first section.
int sectionIndex(Place place) {
  final names = sectionsOf(place.kind);
  final name = place.section;
  if (names == null || name == null) return 0;
  final index = names.indexOf(name);
  return index < 0 ? 0 : index;
}

/// The path segment of each simple place. `project` and `thread` carry a
/// target and are formatted by [formatPlace].
const _segments = {
  PlaceKind.overview: '',
  PlaceKind.inbox: 'inbox',
  PlaceKind.pulls: 'pulls',
  PlaceKind.automations: 'automations',
  PlaceKind.library: 'library',
  PlaceKind.settings: 'settings',
};

/// The place [path] names. Anything this build does not know — a place a
/// later phase adds, a truncated deep link — is the overview: a link that
/// cannot be honoured shows the fleet rather than nothing.
///
/// [path] is the part below the studio's base, so `/inbox` rather than
/// `/next/inbox`; a fragment is ignored, and the query carries the keys in
/// [linkQuery] and no others.
Place parsePlace(String path) {
  final cut = path.indexOf(RegExp(r'[?#]'));
  final bare = cut < 0 ? path : path.substring(0, cut);
  final parts = <String>[];
  for (final raw in bare.split('/')) {
    if (raw.isEmpty) continue;
    try {
      parts.add(Uri.decodeComponent(raw));
    } on ArgumentError {
      // A malformed escape is not a place this build can honour.
      return const Place.overview();
    }
  }
  final query = linkQuery(path);
  final place = _placeOf(parts);
  return query.isEmpty ? place : place.withQuery(query);
}

/// The link keys [path]'s query carries, and no others: a query a person
/// adds by hand does not make a link a different place.
Map<String, String> linkQuery(String path) {
  final start = path.indexOf('?');
  if (start < 0) return const {};
  var end = path.indexOf('#', start);
  if (end < 0) end = path.length;
  final out = <String, String>{};
  try {
    final params = Uri.splitQueryString(path.substring(start + 1, end));
    for (final key in const [linkWidthKey, linkTabKey]) {
      final value = params[key];
      if (value != null && value.isNotEmpty) out[key] = value;
    }
  } on ArgumentError {
    // A malformed query names no link keys; the place still stands.
  } on FormatException {
    // The same, for a percent escape the parser refuses.
  }
  return out.isEmpty ? const {} : Map<String, String>.unmodifiable(out);
}

/// The place [parts] name, with no query.
Place _placeOf(List<String> parts) {
  if (parts.isEmpty) return const Place.overview();

  switch (parts.first) {
    case 'inbox':
      return parts.length == 1 ? const Place(PlaceKind.inbox) : _unknown;
    case 'pulls':
      return parts.length == 1 ? const Place(PlaceKind.pulls) : _unknown;
    case 'automations':
      return _sectioned(PlaceKind.automations, parts);
    case 'library':
      return _sectioned(PlaceKind.library, parts);
    case 'settings':
      return _sectioned(PlaceKind.settings, parts);
    case 'project':
      return parts.length == 2
          ? Place(PlaceKind.project, id: parts[1])
          : _unknown;
    case 'thread':
      // `/thread/<id>` names a thread no host is named for: a link written
      // before the app knew which host answers for it (F8 decision 2), or one
      // a person typed. The shell asks the fleet.
      if (parts.length == 2) return Place(PlaceKind.thread, id: parts[1]);
      return parts.length == 3
          ? Place(PlaceKind.thread, host: parts[1], id: parts[2])
          : _unknown;
  }
  return _unknown;
}

/// The place [parts] name within a place that has sections: the place
/// itself, or one of its sections, and nothing deeper.
Place _sectioned(PlaceKind kind, List<String> parts) {
  if (parts.length == 1) return Place(kind);
  if (parts.length > 2) return _unknown;
  final names = sectionsOf(kind);
  if (names == null || !names.contains(parts[1])) return _unknown;
  return Place(kind, section: parts[1]);
}

const _unknown = Place.overview();

/// The path that names [place], the inverse of [parsePlace] for every place
/// that carries its target, its query keys in order.
String formatPlace(Place place) {
  final segment = _segments[place.kind];
  final section = place.section;
  final path = segment != null
      ? (section == null
            ? '/$segment'
            : '/$segment/${Uri.encodeComponent(section)}')
      : switch (place.kind) {
          PlaceKind.project =>
            '/project/${Uri.encodeComponent(place.id ?? '')}',
          // A thread a link names without its host keeps that form: the path
          // says no more than the link did.
          PlaceKind.thread when place.host == null =>
            '/thread/${Uri.encodeComponent(place.id ?? '')}',
          PlaceKind.thread =>
            '/thread/${Uri.encodeComponent(place.host ?? '')}'
                '/${Uri.encodeComponent(place.id ?? '')}',
          // Unreachable: every simple place is in _segments.
          _ => '/',
        };
  if (place.query.isEmpty) return path;
  final keys = place.query.keys.toList()..sort();
  final query = [
    for (final key in keys)
      '${Uri.encodeQueryComponent(key)}='
          '${Uri.encodeQueryComponent(place.query[key]!)}',
  ].join('&');
  return '$path?$query';
}
