import 'package:flutter/services.dart' show LogicalKeyboardKey;

import '../../model/fleet_store.dart' show HostView;
import '../../model/nav_model.dart';
import '../../model/place.dart';
import '../../model/thread_ref.dart' as model;
import '../kit/kit.dart';
import 'host_switcher.dart';

/// The Studio's navigation (design-system.md §2's layout and the mock's
/// sidebar): the host switcher, the search field, the three fixed places
/// above the tree, the projects with their threads, and the three at its
/// foot.
///
/// It draws the model it is given and never fetches (F2 decision 3): the same
/// [NavModel] makes the same sidebar in a test as on screen.
class Sidebar extends StatefulWidget {
  const Sidebar({
    super.key,
    required this.nav,
    required this.hosts,
    required this.place,
    required this.onSelect,
    this.host,
    this.onHost,
    this.onConnectRepo,
    this.onNew,
    this.now,
    this.inPanel = false,
    this.searchFocus,
  });

  final NavModel nav;
  final List<HostView> hosts;
  final Place place;

  /// Opens a place: a thread, a project, or one of the fixed places.
  final void Function(Place place) onSelect;

  /// The host whose work the tree shows, or null for every host.
  final String? host;
  final void Function(String? host)? onHost;

  /// Connects a repository (F9 B1): the plus beside *Projects*. Null leaves
  /// the control out, so a host that wires no bridge offers no sheet.
  final VoidCallback? onConnectRepo;

  /// The place-level New (F9 B3): the plus beside the search field. Null leaves
  /// it out — there is no project to start work in.
  final VoidCallback? onNew;

  /// What a host's age is measured against.
  final DateTime? now;

  /// Drawn as the narrow layout's panel, which fills its own height rather
  /// than the window's.
  final bool inPanel;

  /// The node the search field's keyboard belongs to, when the shell holds one
  /// so `⌘K` can reach it from anywhere. Null leaves the field its own.
  final FocusNode? searchFocus;

  @override
  State<Sidebar> createState() => _SidebarState();
}

class _SidebarState extends State<Sidebar> {
  /// The projects whose threads are hidden. A project starts open: its work
  /// is what the sidebar is for.
  final _closed = <String>{};

  /// The projects whose earlier threads are shown. A project's earlier work
  /// starts closed: it is there to be found, not to be read.
  final _openEarlier = <String>{};

  /// What is typed in the search field. While it holds something, the sidebar
  /// draws what the search finds in place of its tree.
  final _query = TextEditingController();

  @override
  void dispose() {
    _query.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final now = widget.now ?? DateTime.now();
    final fixed = widget.nav.fixed;
    final above = fixed.take(navAboveProjects).toList();
    final below = fixed.skip(navAboveProjects).toList();
    final tree = _tree();
    final found = _query.text.trim().isEmpty
        ? null
        : searchNav(fixed, tree, _query.text);

    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(
            Space.s12,
            Space.s12,
            Space.s12,
            Space.s8,
          ),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              HostSwitcher(
                hosts: widget.hosts,
                selected: widget.host,
                onSelect: widget.onHost ?? (_) {},
                now: now,
              ),
              const SizedBox(height: Space.s8),
              // The search finds by title: what is typed narrows the sidebar to
              // the places, threads and projects whose words it holds, and
              // Enter opens the first of them. The plus beside it starts work,
              // the way a page header's New does (F9 B3).
              Row(
                crossAxisAlignment: CrossAxisAlignment.end,
                children: [
                  Expanded(
                    child: Tip(
                      message: 'Find a thread, project or place (⌘K)',
                      child: CallbackShortcuts(
                        // Escape is the field's own: it empties the search and
                        // lets the keyboard go, so the tree's shortcuts work
                        // again.
                        bindings: {
                          const SingleActivator(LogicalKeyboardKey.escape):
                              _escape,
                        },
                        child: Field.search(
                          controller: _query,
                          hint: 'Search threads and places',
                          focusNode: widget.searchFocus,
                          onChanged: (_) => setState(() {}),
                          onSubmitted: (text) =>
                              _openFirst(searchNav(fixed, tree, text)),
                        ),
                      ),
                    ),
                  ),
                  if (widget.onNew != null) ...[
                    const SizedBox(width: Space.s8),
                    IconBtn(Glyphs.plus, tooltip: 'New', onTap: widget.onNew),
                  ],
                ],
              ),
            ],
          ),
        ),
        if (found != null)
          Expanded(child: _results(found))
        else ...[
          Padding(
            padding: const EdgeInsets.symmetric(horizontal: Space.s8),
            child: Column(children: [for (final row in above) _row(row)]),
          ),
          const SizedBox(height: Space.s12),
          Expanded(
            child: ListView(
              padding: const EdgeInsets.fromLTRB(
                Space.s8,
                0,
                Space.s8,
                Space.s8,
              ),
              children: [
                Padding(
                  padding: const EdgeInsets.fromLTRB(
                    Space.s4,
                    Space.s6,
                    Space.s4,
                    Space.s4,
                  ),
                  child: Row(
                    children: [
                      const Expanded(child: Label('Projects')),
                      if (widget.onConnectRepo != null)
                        IconBtn(
                          Glyphs.plus,
                          tooltip: 'Connect a repository',
                          onTap: widget.onConnectRepo,
                        ),
                    ],
                  ),
                ),
                for (final project in tree) ..._project(project),
              ],
            ),
          ),
          const Hairline(),
          Padding(
            padding: const EdgeInsets.all(Space.s8),
            child: Column(children: [for (final row in below) _row(row)]),
          ),
        ],
      ],
    );
  }

  /// What the search found, drawn in place of the fixed rows, the tree and the
  /// foot: places, then threads, then projects, and a group with nothing in it
  /// is not drawn at all.
  Widget _results(NavSearch found) {
    if (found.isEmpty) {
      final e = Ember.of(context);
      return Padding(
        padding: const EdgeInsets.fromLTRB(Space.s12, 0, Space.s12, 0),
        child: Text(
          'Nothing is called “${_query.text.trim()}”',
          style: e.small.tint(e.muted),
        ),
      );
    }
    return ListView(
      padding: const EdgeInsets.fromLTRB(Space.s8, 0, Space.s8, Space.s8),
      children: [
        if (found.places.isNotEmpty) ...[
          const _GroupLabel('Places'),
          for (final place in found.places) _row(place),
        ],
        if (found.threads.isNotEmpty) ...[
          const _GroupLabel('Threads'),
          for (final thread in found.threads) _threadRow(thread),
        ],
        if (found.projects.isNotEmpty) ...[
          const _GroupLabel('Projects'),
          for (final project in found.projects) _projectRow(project),
        ],
      ],
    );
  }

  /// The projects the tree shows: every project, or the chosen host's.
  List<NavProject> _tree() {
    final host = widget.host;
    if (host == null) return widget.nav.projects;
    return [
      for (final project in widget.nav.projects)
        if (project.current.any((thread) => thread.host == host) ||
            project.earlierThreads.any((thread) => thread.host == host))
          NavProject(
            id: project.id,
            name: project.name,
            current: [
              for (final thread in project.current)
                if (thread.host == host) thread,
            ],
            earlierThreads: [
              for (final thread in project.earlierThreads)
                if (thread.host == host) thread,
            ],
            active: project.active,
          ),
    ];
  }

  List<Widget> _project(NavProject project) {
    final open = !_closed.contains(project.id);
    final earlier = project.earlier > 0;
    final earlierOpen =
        _openEarlier.contains(project.id) ||
        project.earlierThreads.any(_isOpen);
    return [
      NavRow(
        label: project.name,
        selected: project.active,
        expanded: open,
        onToggle: () => setState(() {
          open ? _closed.add(project.id) : _closed.remove(project.id);
        }),
        onTap: () => _open(Place(PlaceKind.project, id: project.id)),
      ),
      if (open) ...[
        for (final thread in project.current)
          Padding(
            padding: const EdgeInsets.only(left: Space.s16),
            child: _threadRow(thread),
          ),
        if (earlier) ...[
          NavRow(
            label: 'Earlier',
            count: project.earlier,
            indent: true,
            expanded: earlierOpen,
            onToggle: () => _toggleEarlier(project.id),
            onTap: () => _toggleEarlier(project.id),
          ),
          if (earlierOpen)
            for (final thread in project.earlierThreads)
              Padding(
                padding: const EdgeInsets.only(left: Space.s32),
                child: _threadRow(thread),
              ),
        ],
      ],
    ];
  }

  /// One of a project's threads as the tree draws it: a thread opens itself.
  Widget _threadRow(model.ThreadRef thread) => ThreadRow(
    status: _status(thread.status),
    title: thread.title,
    kind: _kind(thread.kind),
    selected: _isOpen(thread),
    onTap: () =>
        _open(Place(PlaceKind.thread, host: thread.host, id: thread.id)),
  );

  /// Opens and closes a project's earlier threads. A group holding the open
  /// thread is drawn open whatever this set says: the row being read is
  /// always visible.
  void _toggleEarlier(String id) => setState(() {
    _openEarlier.contains(id) ? _openEarlier.remove(id) : _openEarlier.add(id);
  });

  bool _isOpen(model.ThreadRef thread) =>
      widget.place.kind == PlaceKind.thread &&
      widget.place.id == thread.id &&
      widget.place.host == thread.host;

  Widget _row(NavPlace row) => NavRow(
    label: row.label,
    icon: _icon(row.kind),
    count: row.count,
    selected: row.active,
    onTap: () => _open(Place(row.kind)),
  );

  /// A project the search found: its folder and its name, opening its page.
  Widget _projectRow(NavProject project) => NavRow(
    label: project.name,
    icon: Glyphs.folder,
    selected: project.active,
    onTap: () => _open(Place(PlaceKind.project, id: project.id)),
  );

  /// Opens [place], and clears the search: what was found is now what is open.
  void _open(Place place) {
    widget.onSelect(place);
    if (_query.text.isNotEmpty) _clear();
  }

  /// Enter: the first entry the search draws — a place, else a thread, else a
  /// project — opens, and the field goes back to empty.
  void _openFirst(NavSearch found) {
    if (found.places.isNotEmpty) {
      _open(Place(found.places.first.kind));
    } else if (found.threads.isNotEmpty) {
      final first = found.threads.first;
      _open(Place(PlaceKind.thread, host: first.host, id: first.id));
    } else if (found.projects.isNotEmpty) {
      _open(Place(PlaceKind.project, id: found.projects.first.id));
    } else {
      // Nothing was found to open: the field still empties, so the tree the
      // person was looking for is one keystroke away.
      _clear();
    }
  }

  /// Empties the field, so the sidebar draws its tree again.
  void _clear() {
    _query.clear();
    setState(() {});
  }

  /// Escape empties the search and lets go of the keyboard, so the tree's own
  /// shortcuts work again: the field is the only thing Escape is bound to.
  void _escape() {
    _clear();
    FocusScope.of(context).unfocus();
  }

  /// The mark each fixed place carries (§6: a kind is an icon and a tooltip,
  /// and a place is its icon here).
  static IconData _icon(PlaceKind kind) => switch (kind) {
    PlaceKind.overview => Glyphs.square_grid_2x2,
    PlaceKind.inbox => Glyphs.tray,
    PlaceKind.pulls => Glyphs.arrow_merge,
    PlaceKind.automations => Glyphs.bolt,
    PlaceKind.library => Glyphs.book,
    PlaceKind.settings => Glyphs.gear,
    PlaceKind.project => Glyphs.folder,
    PlaceKind.thread => Glyphs.chat_bubble,
  };

  static Status _status(model.ThreadStatus status) => switch (status) {
    model.ThreadStatus.needsYou => Status.needsYou,
    model.ThreadStatus.running => Status.running,
    model.ThreadStatus.ready => Status.ready,
    model.ThreadStatus.failed => Status.failed,
    model.ThreadStatus.idle => Status.idle,
  };

  static ThreadKind _kind(model.ThreadKind kind) => switch (kind) {
    model.ThreadKind.agent => ThreadKind.agent,
    model.ThreadKind.workItem => ThreadKind.workItem,
    model.ThreadKind.planRun => ThreadKind.planRun,
    model.ThreadKind.session => ThreadKind.session,
  };
}

/// The label above one group of search results, spaced as the tree's own
/// `Projects` label is.
class _GroupLabel extends StatelessWidget {
  const _GroupLabel(this.text);

  final String text;

  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.fromLTRB(Space.s4, Space.s6, Space.s4, Space.s4),
    child: Label(text),
  );
}
