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
      return (
        'Needs you',
        'The Needs you screen arrives with the next step of this phase',
      );
    case PlaceKind.pulls:
      return (
        'Pull requests',
        'The Pull requests screen is built; it opens once the shell wires the '
            'per-host bridge',
      );
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
      // The thread screen is built (F3), so a place this screen cannot open is
      // never a phase's promise. When a hostless link or legacy `#runs/<id>`
      // names an id no host answers for, the honest line is that nothing
      // lists it.
      if (thread == null) {
        return (place.id ?? '', 'No host in this fleet lists that thread');
      }
      return (
        thread.title,
        'The thread screen is built; it opens once the shell wires the '
            'per-host bridge',
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

/// The thread a place names, when the sidebar's fleet lists it.
///
/// A place that carries no host of its own — a link written for the one-bridge
/// Studio (F8 decision 2) — is answered by whichever host lists the id, as the
/// shell's own lookup does.
model.ThreadRef? _thread(Place place, NavModel nav) {
  for (final project in nav.projects) {
    for (final thread in project.current) {
      if (thread.id != place.id) continue;
      if (place.host == null || thread.host == place.host) return thread;
    }
  }
  return null;
}
