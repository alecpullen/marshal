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

/// One place, with the target it names: a project's id, or a thread's host
/// and id.
///
/// In the browser the place and its target are the URL path under the
/// studio's base (`/next/`), so a reload and Back work (F2 decision 4); the
/// same parser serves a deep link on the desktop, where there is no URL.
@immutable
class Place {
  const Place(this.kind, {this.host, this.id})
    : assert(
        kind != PlaceKind.project || id != null,
        'a project place names its project',
      ),
      assert(
        kind != PlaceKind.thread || (host != null && id != null),
        'a thread place names its host and its thread',
      );

  /// The overview: the fleet at a glance.
  const Place.overview() : this(PlaceKind.overview);

  /// The inbox: every ask, oldest first.
  const Place.inbox() : this(PlaceKind.inbox);

  final PlaceKind kind;

  /// The host a thread belongs to. Every other place spans hosts.
  final String? host;

  /// The project's id (its root) or the thread's id.
  final String? id;

  @override
  bool operator ==(Object other) =>
      other is Place &&
      other.kind == kind &&
      other.host == host &&
      other.id == id;

  @override
  int get hashCode => Object.hash(kind, host, id);

  @override
  String toString() =>
      'Place(${kind.name}${host == null ? '' : ', $host'}'
      '${id == null ? '' : ', $id'})';
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
/// `/next/inbox`; a query and a fragment are ignored.
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
  if (parts.isEmpty) return const Place.overview();

  switch (parts.first) {
    case 'inbox':
      return parts.length == 1 ? const Place(PlaceKind.inbox) : _unknown;
    case 'pulls':
      return parts.length == 1 ? const Place(PlaceKind.pulls) : _unknown;
    case 'automations':
      return parts.length == 1 ? const Place(PlaceKind.automations) : _unknown;
    case 'library':
      return parts.length == 1 ? const Place(PlaceKind.library) : _unknown;
    case 'settings':
      return parts.length == 1 ? const Place(PlaceKind.settings) : _unknown;
    case 'project':
      return parts.length == 2
          ? Place(PlaceKind.project, id: parts[1])
          : _unknown;
    case 'thread':
      return parts.length == 3
          ? Place(PlaceKind.thread, host: parts[1], id: parts[2])
          : _unknown;
  }
  return _unknown;
}

const _unknown = Place.overview();

/// The path that names [place], the inverse of [parsePlace] for every place
/// that carries its target.
String formatPlace(Place place) {
  final segment = _segments[place.kind];
  if (segment != null) return '/$segment';
  return switch (place.kind) {
    PlaceKind.project => '/project/${Uri.encodeComponent(place.id ?? '')}',
    PlaceKind.thread =>
      '/thread/${Uri.encodeComponent(place.host ?? '')}'
          '/${Uri.encodeComponent(place.id ?? '')}',
    // Unreachable: every simple place is in _segments.
    _ => '/',
  };
}
