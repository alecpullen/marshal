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
    this.now,
    this.inPanel = false,
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

  /// What a host's age is measured against.
  final DateTime? now;

  /// Drawn as the narrow layout's panel, which fills its own height rather
  /// than the window's.
  final bool inPanel;

  @override
  State<Sidebar> createState() => _SidebarState();
}

class _SidebarState extends State<Sidebar> {
  /// The projects whose threads are hidden. A project starts open: its work
  /// is what the sidebar is for.
  final _closed = <String>{};

  @override
  Widget build(BuildContext context) {
    final now = widget.now ?? DateTime.now();
    final fixed = widget.nav.fixed;
    final above = fixed.take(navAboveProjects).toList();
    final below = fixed.skip(navAboveProjects).toList();
    final tree = _tree();

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
              // Search arrives in a later phase; the field is drawn where it
              // will live and says so rather than pretending to search.
              const Tip(
                message: 'Search arrives in a later phase',
                child: Field(
                  label: 'Search',
                  hint: 'Search or jump to',
                  enabled: false,
                ),
              ),
            ],
          ),
        ),
        Padding(
          padding: const EdgeInsets.symmetric(horizontal: Space.s8),
          child: Column(children: [for (final row in above) _row(row)]),
        ),
        const SizedBox(height: Space.s12),
        Expanded(
          child: ListView(
            padding: const EdgeInsets.fromLTRB(Space.s8, 0, Space.s8, Space.s8),
            children: [
              Padding(
                padding: const EdgeInsets.fromLTRB(
                  Space.s8,
                  Space.s6,
                  Space.s8,
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
    );
  }

  /// The projects the tree shows: every project, or the chosen host's.
  List<NavProject> _tree() {
    final host = widget.host;
    if (host == null) return widget.nav.projects;
    return [
      for (final project in widget.nav.projects)
        if (project.current.any((thread) => thread.host == host))
          NavProject(
            id: project.id,
            name: project.name,
            current: [
              for (final thread in project.current)
                if (thread.host == host) thread,
            ],
            earlier: project.earlier,
            active: project.active,
          ),
    ];
  }

  List<Widget> _project(NavProject project) {
    final open = !_closed.contains(project.id);
    return [
      NavRow(
        label: project.name,
        selected: project.active,
        expanded: open,
        onToggle: () => setState(() {
          open ? _closed.add(project.id) : _closed.remove(project.id);
        }),
        onTap: () => widget.onSelect(Place(PlaceKind.project, id: project.id)),
      ),
      if (open) ...[
        for (final thread in project.current)
          Padding(
            padding: const EdgeInsets.only(left: Space.s16),
            child: ThreadRow(
              status: _status(thread.status),
              title: thread.title,
              kind: _kind(thread.kind),
              selected: _isOpen(thread),
              onTap: () => widget.onSelect(
                Place(PlaceKind.thread, host: thread.host, id: thread.id),
              ),
            ),
          ),
        if (project.earlier > 0)
          Padding(
            padding: const EdgeInsets.fromLTRB(
              Space.s32,
              Space.s2,
              Space.s8,
              Space.s8,
            ),
            child: Text(
              'Earlier · ${project.earlier}',
              style: Ember.of(context).small.tint(Ember.of(context).faint),
            ),
          ),
      ],
    ];
  }

  bool _isOpen(model.ThreadRef thread) =>
      widget.place.kind == PlaceKind.thread &&
      widget.place.id == thread.id &&
      widget.place.host == thread.host;

  Widget _row(NavPlace row) => NavRow(
    label: row.label,
    icon: _icon(row.kind),
    count: row.count,
    selected: row.active,
    onTap: () => widget.onSelect(Place(row.kind)),
  );

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
  };
}
