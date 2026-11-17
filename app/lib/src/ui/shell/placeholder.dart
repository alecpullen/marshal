import '../../model/nav_model.dart';
import '../../model/place.dart';
import '../../model/thread_ref.dart' as model;
import '../kit/kit.dart';

/// A place this phase does not build yet, drawn as §7 requires: a disabled
/// row where it will live, with one line naming the phase that brings it. It
/// is never drawn as working.
Widget plannedPlace(Place place, NavModel nav) {
  final (name, note) = _about(place, nav);
  return SingleChildScrollView(
    padding: const EdgeInsets.all(Layout.pageSide),
    child: StateMessage.planned(name: name, note: note),
  );
}

/// What a place is called, wherever its name is written.
String placeTitle(Place place, NavModel nav) => _about(place, nav).$1;

/// The name and the phase's promise for [place].
(String, String) _about(Place place, NavModel nav) {
  switch (place.kind) {
    case PlaceKind.overview:
      // F2's Overview lands with the next step of this phase.
      return (
        'Overview',
        'The Overview arrives with the next step of this phase',
      );
    case PlaceKind.inbox:
      return ('Inbox', 'The Inbox arrives with the next step of this phase');
    case PlaceKind.pulls:
      return ('Pull requests', 'Pull requests arrive in a later phase');
    case PlaceKind.automations:
      return (
        'Automations',
        'The Automations screen is built; it opens once the shell wires the '
            'per-host bridge',
      );
    case PlaceKind.library:
      return (
        'Library',
        'The Library screen is built; it opens once the shell wires the '
            'per-host bridge',
      );
    case PlaceKind.settings:
      return ('Settings', 'Settings arrive in a later phase');
    case PlaceKind.project:
      final id = place.id ?? '';
      final name = _projectName(id, nav);
      return (
        name,
        'A project’s own page arrives in F3; its threads are in the sidebar',
      );
    case PlaceKind.thread:
      final thread = _thread(place, nav);
      return (
        thread?.title ?? place.id ?? '',
        'The thread screen arrives in F3',
      );
  }
}

/// What a project is called, when the fleet still reports it.
String _projectName(String id, NavModel nav) {
  for (final project in nav.projects) {
    if (project.id == id) return project.name;
  }
  return id;
}

/// The thread a place names, when the fleet still reports it.
model.ThreadRef? _thread(Place place, NavModel nav) {
  for (final project in nav.projects) {
    for (final thread in project.current) {
      if (thread.id == place.id && thread.host == place.host) return thread;
    }
  }
  return null;
}
