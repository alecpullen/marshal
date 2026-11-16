import 'package:cupertino_ui/cupertino_ui.dart' show CupertinoIcons;
import 'package:material_ui/material_ui.dart';
import 'package:flutter/services.dart';

import 'composer.dart';
import 'data.dart';
import 'inbox.dart';
import 'overview.dart';
import 'places.dart';
import 'project.dart';
import 'pulls.dart';
import 'state.dart';
import 'thread.dart';
import 'widgets.dart';

void main() => runApp(const StudioMock());

class StudioMock extends StatefulWidget {
  const StudioMock({super.key});

  @override
  State<StudioMock> createState() => _StudioMockState();
}

class _StudioMockState extends State<StudioMock> {
  final state = StudioState();

  @override
  Widget build(BuildContext context) {
    return StudioScope(
      state: state,
      child: Builder(builder: (context) {
        final s = context.s;
        return MaterialApp(
          title: 'Marshal Studio',
          debugShowCheckedModeBanner: false,
          theme: ThemeData(
            brightness: s.dark ? Brightness.dark : Brightness.light,
            scaffoldBackgroundColor: s.bg,
            splashFactory: NoSplash.splashFactory,
            colorScheme: ColorScheme.fromSeed(
              seedColor: s.accent,
              brightness: s.dark ? Brightness.dark : Brightness.light,
            ),
            tooltipTheme: const TooltipThemeData(waitDuration: Duration(milliseconds: 500)),
          ),
          home: const Shell(),
        );
      }),
    );
  }
}

class Shell extends StatefulWidget {
  const Shell({super.key});

  @override
  State<Shell> createState() => _ShellState();
}

class _ShellState extends State<Shell> {
  @override
  void initState() {
    super.initState();
    // `?new=1` opens the composer straight away, so it can be linked to.
    if (Uri.base.queryParameters['new'] == '1') {
      WidgetsBinding.instance.addPostFrameCallback((_) => showComposer(context));
    }
  }

  @override
  Widget build(BuildContext context) {
    final studio = context.studio;
    final s = context.s;
    return CallbackShortcuts(
      bindings: {
        const SingleActivator(LogicalKeyboardKey.keyN, meta: true): () => showComposer(context),
        const SingleActivator(LogicalKeyboardKey.backslash, meta: true): () =>
            studio.set(() => studio.inspectorOpen = !studio.inspectorOpen),
        const SingleActivator(LogicalKeyboardKey.digit1, meta: true): () => studio.go(Place.overview),
        const SingleActivator(LogicalKeyboardKey.digit2, meta: true): () => studio.go(Place.inbox),
        const SingleActivator(LogicalKeyboardKey.digit3, meta: true): () => studio.go(Place.pulls),
      },
      child: Focus(
        autofocus: true,
        child: DefaultTextStyle(
          style: s.text(13),
          child: LayoutBuilder(builder: (context, box) {
            final narrow = box.maxWidth < 760;
            final content = switch (studio.place) {
              Place.overview => const Overview(),
              Place.inbox => const Inbox(),
              Place.pulls => const PullsPage(),
              Place.project => ProjectPage(project: projectById(studio.id)),
              Place.thread => ThreadPage(thread: threadById(studio.id), narrow: box.maxWidth < 1080),
              Place.automations => const Automations(),
              Place.library => const Library(),
              Place.settings => const Settings(),
            };
            if (narrow) {
              return Scaffold(
                drawer: Drawer(
                  backgroundColor: s.sidebar,
                  shape: const RoundedRectangleBorder(),
                  width: 272,
                  child: const SafeArea(child: Sidebar(inDrawer: true)),
                ),
                body: SafeArea(
                  child: Column(children: [
                    Container(
                      height: 44,
                      padding: const EdgeInsets.symmetric(horizontal: 8),
                      decoration: BoxDecoration(border: Border(bottom: BorderSide(color: s.border))),
                      child: Row(children: [
                        Builder(
                          builder: (context) => IconBtn(CupertinoIcons.sidebar_left,
                              tooltip: 'Projects and threads',
                              onTap: () => Scaffold.of(context).openDrawer()),
                        ),
                        const SizedBox(width: 6),
                        Text('Marshal Studio', style: s.text(13, weight: FontWeight.w600)),
                        const Spacer(),
                        IconBtn(CupertinoIcons.tray, tooltip: 'Inbox', onTap: () => studio.go(Place.inbox)),
                        IconBtn(CupertinoIcons.plus, tooltip: 'New  ⌘N', onTap: () => showComposer(context)),
                      ]),
                    ),
                    Expanded(child: content),
                  ]),
                ),
              );
            }
            return Scaffold(
              body: Row(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
                const SizedBox(width: 252, child: Sidebar()),
                const Hairline(vertical: true),
                Expanded(child: content),
              ]),
            );
          }),
        ),
      ),
    );
  }
}

/// The whole of navigation: two fixed places on top, the projects and their
/// threads in the middle, three fixed places at the foot.
class Sidebar extends StatelessWidget {
  const Sidebar({super.key, this.inDrawer = false});
  final bool inDrawer;

  @override
  Widget build(BuildContext context) {
    final studio = context.studio;
    final s = context.s;

    void go(Place place, [String id = '']) {
      studio.go(place, id);
      if (inDrawer) Navigator.of(context).pop();
    }

    return Container(
      color: s.sidebar,
      child: Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(12, 12, 12, 8),
          child: Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
            const _HostSwitcher(),
            const SizedBox(height: 10),
            Row(children: [
              Expanded(
                child: Container(
                  height: 30,
                  padding: const EdgeInsets.symmetric(horizontal: 9),
                  decoration: BoxDecoration(
                    border: Border.all(color: s.border),
                    borderRadius: BorderRadius.circular(s.control),
                  ),
                  child: Row(children: [
                    Icon(CupertinoIcons.search, size: 13, color: s.faint),
                    const SizedBox(width: 7),
                    Expanded(child: Text('Search or jump to', style: s.text(12.5, color: s.faint))),
                    Text('⌘K', style: s.mono(11, color: s.faint)),
                  ]),
                ),
              ),
              const SizedBox(width: 6),
              Tooltip(
                message: 'New  ⌘N',
                child: Tap(
                  onTap: () => showComposer(context),
                  builder: (context, hovered) => Container(
                    width: 30,
                    height: 30,
                    decoration: BoxDecoration(
                      color: s.primary.withValues(alpha: hovered ? 0.88 : 1),
                      borderRadius: BorderRadius.circular(s.control),
                    ),
                    child: Icon(CupertinoIcons.plus, size: 15, color: s.onPrimary),
                  ),
                ),
              ),
            ]),
          ]),
        ),
        Padding(
          padding: const EdgeInsets.symmetric(horizontal: 8),
          child: Column(children: [
            _NavRow(
              icon: CupertinoIcons.square_grid_2x2,
              label: 'Overview',
              active: studio.place == Place.overview,
              onTap: () => go(Place.overview),
            ),
            _NavRow(
              icon: CupertinoIcons.tray,
              label: 'Inbox',
              active: studio.place == Place.inbox,
              badge: '${inbox.length}',
              badgeColor: s.warn,
              onTap: () => go(Place.inbox),
            ),
            _NavRow(
              icon: CupertinoIcons.arrow_merge,
              label: 'Pull requests',
              active: studio.place == Place.pulls,
              badge: '${pulls.where((p) => p.ready).length}',
              badgeColor: s.ok,
              onTap: () => go(Place.pulls),
            ),
          ]),
        ),
        const SizedBox(height: 10),
        Expanded(
          child: ListView(
            padding: const EdgeInsets.fromLTRB(8, 0, 8, 8),
            children: [
              Padding(
                padding: const EdgeInsets.fromLTRB(8, 6, 4, 4),
                child: Label('Projects', trailing: IconBtn(CupertinoIcons.plus, tooltip: 'Connect a repository')),
              ),
              for (final project in projects) ...[
                _ProjectRow(project: project, onOpen: () => go(Place.project, project.id)),
                if (!studio.collapsed.contains(project.id)) ...[
                  for (final thread in threads.where((t) => t.project == project.id))
                    _ThreadRow(
                      thread: thread,
                      active: studio.place == Place.thread && studio.id == thread.id,
                      onTap: () => go(Place.thread, thread.id),
                    ),
                  Padding(
                    padding: const EdgeInsets.fromLTRB(30, 3, 8, 8),
                    child: Text('Earlier · ${project.id == 'marshal' ? 23 : 6}', style: s.text(11.5, color: s.faint)),
                  ),
                ],
              ],
            ],
          ),
        ),
        const Hairline(),
        Padding(
          padding: const EdgeInsets.all(8),
          child: Column(children: [
            _NavRow(
              icon: CupertinoIcons.bolt,
              label: 'Automations',
              active: studio.place == Place.automations,
              onTap: () => go(Place.automations),
            ),
            _NavRow(
              icon: CupertinoIcons.book,
              label: 'Library',
              active: studio.place == Place.library,
              onTap: () => go(Place.library),
            ),
            _NavRow(
              icon: CupertinoIcons.gear,
              label: 'Settings',
              active: studio.place == Place.settings,
              onTap: () => go(Place.settings),
            ),
          ]),
        ),
        const Hairline(),
        const _MockControls(),
      ]),
    );
  }
}

class _HostSwitcher extends StatelessWidget {
  const _HostSwitcher();

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    return PopupMenuButton<String>(
      tooltip: 'Switch daemon or server',
      color: s.bg,
      elevation: 3,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(s.radius),
        side: BorderSide(color: s.border),
      ),
      position: PopupMenuPosition.under,
      itemBuilder: (context) => [
        for (final host in const [
          ('studio-box', 'server · 6 of 8 slots', true),
          ('Local daemon', 'this Mac · 1 of 2 slots', true),
          ('vps-syd', 'server · unreachable 4m', false),
        ])
          PopupMenuItem(
            value: host.$1,
            height: 44,
            child: Row(children: [
              Dot(host.$3 ? Status.ready : Status.failed, size: 7),
              const SizedBox(width: 9),
              Column(crossAxisAlignment: CrossAxisAlignment.start, mainAxisSize: MainAxisSize.min, children: [
                Text(host.$1, style: s.text(12.5, weight: FontWeight.w500)),
                Text(host.$2, style: s.text(11, color: s.muted)),
              ]),
            ]),
          ),
        PopupMenuItem(
          value: 'add',
          height: 36,
          child: Text('Add a host…', style: s.text(12.5, color: s.muted)),
        ),
      ],
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 4, vertical: 2),
        child: Row(children: [
          Container(
            width: 22,
            height: 22,
            alignment: Alignment.center,
            decoration: BoxDecoration(color: s.fg, borderRadius: BorderRadius.circular(6)),
            child: Text('m', style: s.text(13, color: s.bg, weight: FontWeight.w700)),
          ),
          const SizedBox(width: 9),
          Expanded(
            child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Text('Marshal Studio', style: s.text(13, weight: FontWeight.w600)),
              Text('All hosts · 2 of 3 online', style: s.text(11, color: s.muted)),
            ]),
          ),
          Icon(CupertinoIcons.chevron_up_chevron_down, size: 12, color: s.faint),
        ]),
      ),
    );
  }
}

class _NavRow extends StatelessWidget {
  const _NavRow({required this.icon, required this.label, required this.active, required this.onTap, this.badge, this.badgeColor});
  final IconData icon;
  final String label;
  final bool active;
  final VoidCallback onTap;
  final String? badge;
  final Color? badgeColor;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    return Tap(
      onTap: onTap,
      builder: (context, hovered) => Container(
        height: 30,
        padding: const EdgeInsets.symmetric(horizontal: 8),
        decoration: BoxDecoration(
          color: active ? s.selected : (hovered ? s.hover : null),
          borderRadius: BorderRadius.circular(s.control),
        ),
        child: Row(children: [
          Icon(icon, size: 15, color: active ? s.accent : s.muted),
          const SizedBox(width: 9),
          Expanded(child: Text(label, style: s.text(13, weight: active ? FontWeight.w600 : FontWeight.w400))),
          if (badge != null) Text(badge!, style: s.mono(11.5, color: badgeColor ?? s.muted, weight: FontWeight.w600)),
        ]),
      ),
    );
  }
}

class _ProjectRow extends StatelessWidget {
  const _ProjectRow({required this.project, required this.onOpen});
  final Project project;
  final VoidCallback onOpen;

  @override
  Widget build(BuildContext context) {
    final studio = context.studio;
    final s = context.s;
    final open = !studio.collapsed.contains(project.id);
    final active = studio.place == Place.project && studio.id == project.id;
    return Tap(
      onTap: onOpen,
      builder: (context, hovered) => Container(
        height: 30,
        margin: const EdgeInsets.only(top: 2),
        padding: const EdgeInsets.only(left: 2, right: 8),
        decoration: BoxDecoration(
          color: active ? s.selected : (hovered ? s.hover : null),
          borderRadius: BorderRadius.circular(s.control),
        ),
        child: Row(children: [
          Tooltip(
            message: open ? 'Collapse' : 'Expand',
            child: GestureDetector(
            behavior: HitTestBehavior.opaque,
            onTap: () => studio.set(() => open ? studio.collapsed.add(project.id) : studio.collapsed.remove(project.id)),
            child: SizedBox(
              width: 22,
              height: 30,
              child: Icon(open ? CupertinoIcons.chevron_down : CupertinoIcons.chevron_right, size: 10, color: s.faint),
            ),
          ),
          ),
          Expanded(
            child: Text(project.name,
                overflow: TextOverflow.ellipsis, style: s.text(13, weight: FontWeight.w600)),
          ),
          if (hovered) Icon(CupertinoIcons.arrow_right, size: 11, color: s.faint),
        ]),
      ),
    );
  }
}

class _ThreadRow extends StatelessWidget {
  const _ThreadRow({required this.thread, required this.active, required this.onTap});
  final Thread thread;
  final bool active;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    return Tap(
      onTap: onTap,
      builder: (context, hovered) => Container(
        height: 28,
        padding: const EdgeInsets.only(left: 24, right: 8),
        decoration: BoxDecoration(
          color: active ? s.selected : (hovered ? s.hover : null),
          borderRadius: BorderRadius.circular(s.control),
        ),
        child: Row(children: [
          Dot(thread.status, size: 7),
          const SizedBox(width: 9),
          Expanded(
            child: Text(thread.title,
                overflow: TextOverflow.ellipsis,
                style: s.text(12.5, color: active ? s.fg : s.fg.withValues(alpha: 0.86))),
          ),
          const SizedBox(width: 6),
          if (thread.kind != Kind.agent)
            Tooltip(
              message: kindLabel(thread.kind),
              child: Icon(kindIcon(thread.kind), size: 11, color: s.faint),
            ),
        ]),
      ),
    );
  }
}

/// Mock-only: flips between the two visual directions and light or dark.
class _MockControls extends StatelessWidget {
  const _MockControls();

  @override
  Widget build(BuildContext context) {
    final studio = context.studio;
    final s = context.s;

    Widget choice(String label, bool on, VoidCallback onTap) => Expanded(
          child: Tap(
            onTap: onTap,
            builder: (context, hovered) => Container(
              height: 24,
              alignment: Alignment.center,
              decoration: BoxDecoration(
                color: on ? s.selected : null,
                borderRadius: BorderRadius.circular(s.control - 2),
              ),
              child: Text(label, style: s.text(11.5, color: on ? s.fg : s.muted, weight: on ? FontWeight.w600 : FontWeight.w400)),
            ),
          ),
        );

    Widget group(List<Widget> children) => Container(
          padding: const EdgeInsets.all(2),
          decoration: BoxDecoration(
            border: Border.all(color: s.border),
            borderRadius: BorderRadius.circular(s.control),
          ),
          child: Row(children: children),
        );

    return Padding(
      padding: const EdgeInsets.fromLTRB(12, 8, 12, 10),
      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Text('Mock controls', style: s.text(10.5, color: s.faint)),
        group([
          choice('Light', !studio.dark, () => studio.set(() => studio.dark = false)),
          choice('Dark', studio.dark, () => studio.set(() => studio.dark = true)),
        ]),
      ]),
    );
  }
}
