import 'package:flutter/foundation.dart';

import '../client/contract.g.dart';
import 'factory_store.dart';
import 'fleet_store.dart';
import 'place.dart';
import 'thread_ref.dart';
import 'work_item_model.dart' show threadOfItem;

/// The fixed places that sit above the projects in the sidebar; the rest of
/// [NavModel.fixed] sits below them, as the mock's sidebar does (Overview,
/// Inbox and Pull requests above the tree; Automations, Library and Settings
/// at its foot).
const navAboveProjects = 3;

/// How old an idle thread must be to count as earlier (F2 Decisions): an
/// agent that is idle and was last active more than a day ago.
const earlierAfter = Duration(days: 1);

/// One fixed place as the sidebar draws it.
@immutable
class NavPlace {
  const NavPlace({
    required this.kind,
    required this.label,
    this.count,
    this.active = false,
    this.planned = false,
  });

  final PlaceKind kind;
  final String label;

  /// What the row shows beside its label: the Inbox's queue length.
  final int? count;

  /// This place is where the person is.
  final bool active;

  /// The place has no screen yet; its row opens the message naming the phase
  /// that brings it (§7: never drawn as working).
  final bool planned;

  @override
  bool operator ==(Object other) =>
      other is NavPlace &&
      other.kind == kind &&
      other.label == label &&
      other.count == count &&
      other.active == active &&
      other.planned == planned;

  @override
  int get hashCode => Object.hash(kind, label, count, active, planned);

  @override
  String toString() =>
      'NavPlace(${kind.name}, "$label"'
      '${count == null ? '' : ', $count'}'
      '${active ? ', active' : ''}${planned ? ', planned' : ''})';
}

/// One project row: the project's name, its current threads, and how many
/// threads have gone quiet, drawn as "Earlier · N".
@immutable
class NavProject {
  const NavProject({
    required this.id,
    required this.name,
    required this.current,
    required this.earlier,
    this.active = false,
  });

  /// The project's id: its root, as both hosts and places name it.
  final String id;

  /// What the row calls it: its directory's name.
  final String name;

  /// Its threads now, most recently active first.
  final List<ThreadRef> current;

  /// How many of its threads are idle and older than a day.
  final int earlier;

  /// A thread of this project, or the project itself, is open.
  final bool active;

  @override
  String toString() =>
      'NavProject($id, ${current.length} current, $earlier earlier)';
}

/// What the sidebar draws, computed from the place and the store (F2
/// decision 3). Everything here is pure: the same place and store give the
/// same model, in a test as on screen.
@immutable
class NavModel {
  const NavModel({required this.fixed, required this.projects});

  /// The six fixed places in the design's order: the first
  /// [navAboveProjects] sit above the projects, the rest below.
  final List<NavPlace> fixed;

  /// Every project, in name order.
  final List<NavProject> projects;
}

/// Whether [thread] belongs under a project's "Earlier".
bool isEarlier(ThreadRef thread, DateTime now) =>
    thread.status == ThreadStatus.idle &&
    now.difference(thread.lastActive) > earlierAfter;

/// The sidebar for [place], from what [store] holds.
///
/// [pullsReady] is how many pull requests wait for review: the Pull requests
/// row carries it (F5 decision 14), and the row is no longer planned — the
/// place exists.
///
/// [factory] is the work items, when the app holds them: they appear under
/// their project, and the agents that work them are left out of the agent rows
/// (F6 decision 13).
NavModel buildNavModel(
  Place place,
  FleetStore store, {
  int pullsReady = 0,
  FactoryStore? factory,
}) {
  final now = store.now;
  final inboxCount = store.inbox().length;
  final fixed = [
    NavPlace(
      kind: PlaceKind.overview,
      label: 'Overview',
      active: place.kind == PlaceKind.overview,
    ),
    NavPlace(
      kind: PlaceKind.inbox,
      label: 'Inbox',
      count: inboxCount,
      active: place.kind == PlaceKind.inbox,
    ),
    NavPlace(
      kind: PlaceKind.pulls,
      label: _label(PlaceKind.pulls),
      count: pullsReady,
      active: place.kind == PlaceKind.pulls,
    ),
    for (final kind in const [
      PlaceKind.automations,
      PlaceKind.library,
      PlaceKind.settings,
    ])
      NavPlace(
        kind: kind,
        label: _label(kind),
        // Every one of these arrives in a later phase; until then its row
        // opens the message that says so.
        planned: true,
        active: place.kind == kind,
      ),
  ];

  // A project live on two hosts is one row: the app is one fleet, and each
  // thread carries the host it belongs to.
  //
  // A factory worker is not an independent agent (F6 decision 13): every agent
  // that is a worker of any item, current or historical, is left out here and
  // reached through the item's thread instead.
  final workerIds = factory?.workerAgentIds ?? const <String>{};
  final byProject = <String, _ProjectBuilder>{};
  for (final host in store.hosts) {
    final items = [
      for (final item
          in factory?.items(host: host.name) ?? const <FactoryItem>[])
        threadOfItem(host.name, item),
    ];
    final threads = [
      for (final agent in store.agents(host: host.name))
        if (!workerIds.contains(agent.id)) threadFor(host.name, agent),
      ...items,
    ];
    final grouped = <String>{};
    for (final project in store.projects(host: host.name)) {
      grouped.add(project.root);
      byProject
          .putIfAbsent(project.root, () => _ProjectBuilder(project.root))
          .add(threads.where((thread) => thread.project == project.root));
    }
    // A work item whose project the host has not listed still belongs to it:
    // an item nothing shows is work nobody can find.
    for (final thread in items) {
      if (grouped.contains(thread.project)) continue;
      byProject
          .putIfAbsent(thread.project, () => _ProjectBuilder(thread.project))
          .add([thread]);
    }
  }

  final projects =
      [for (final builder in byProject.values) builder.build(place, now)]
        ..sort((a, b) {
          final byName = a.name.toLowerCase().compareTo(b.name.toLowerCase());
          return byName != 0 ? byName : a.id.compareTo(b.id);
        });

  return NavModel(fixed: List.unmodifiable(fixed), projects: projects);
}

/// What each fixed place is called.
String _label(PlaceKind kind) => switch (kind) {
  PlaceKind.overview => 'Overview',
  PlaceKind.inbox => 'Inbox',
  PlaceKind.pulls => 'Pull requests',
  PlaceKind.project => 'Project',
  PlaceKind.thread => 'Thread',
  PlaceKind.automations => 'Automations',
  PlaceKind.library => 'Library',
  PlaceKind.settings => 'Settings',
};

/// Gathers one project's threads from every host before it is sorted.
class _ProjectBuilder {
  _ProjectBuilder(this.root);

  final String root;
  final _threads = <ThreadRef>[];

  void add(Iterable<ThreadRef> threads) => _threads.addAll(threads);

  NavProject build(Place place, DateTime now) {
    final current = [
      for (final thread in _threads)
        if (!isEarlier(thread, now)) thread,
    ];
    current.sort((a, b) {
      final byTime = b.lastActive.compareTo(a.lastActive);
      return byTime != 0 ? byTime : a.id.compareTo(b.id);
    });
    final earlier = _threads.length - current.length;
    final active =
        (place.kind == PlaceKind.project && place.id == root) ||
        (place.kind == PlaceKind.thread &&
            current.any((thread) => _isOpen(place, thread)));
    return NavProject(
      id: root,
      name: projectName(root),
      current: List.unmodifiable(current),
      earlier: earlier,
      active: active,
    );
  }

  static bool _isOpen(Place place, ThreadRef thread) =>
      place.id == thread.id && place.host == thread.host;
}

/// What a project's row calls it: its directory's name.
///
/// `/home/alec/marshal` is `marshal`; a root with no name of its own — `/` —
/// is named as it is, so the row is never blank.
String projectName(String root) {
  final parts = [
    for (final part in root.split('/'))
      if (part.isNotEmpty) part,
  ];
  return parts.isEmpty ? root : parts.last;
}
