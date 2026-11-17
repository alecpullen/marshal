import '../kit/kit.dart';
import 'library_place.dart';

/// The MCP servers a person configures in the app config: the bridge reports
/// no route for them yet, so the row is planned and says so
/// (design-system.md §7, F7 decision 9). It is never drawn as working.
RowItem plannedMcpServers() => const RowItem.planned(
  name: 'MCP servers',
  description:
      'The bridge lists no MCP servers yet; they are configured in the app '
      'config, and this row is where they will appear.',
);

/// A Library tab this build cannot read: Memory on a fleet that lists no
/// project. §7's planned state — a disabled panel and one line saying why —
/// never a working screen.
class PlannedLibraryTab extends StatelessWidget {
  const PlannedLibraryTab({
    super.key,
    required this.name,
    required this.note,
    required this.tabs,
  });

  final String name;
  final String note;
  final LibraryTabs tabs;

  @override
  Widget build(BuildContext context) {
    return PageBody(
      title: name,
      width: PageWidth.list,
      children: [
        Tabs(tabs: tabs.items, index: tabs.index, onChanged: tabs.onChanged),
        const SizedBox(height: Space.s20),
        StateMessage.planned(name: name, note: note),
      ],
    );
  }
}

/// The builder of a Library tab this build cannot read.
LibraryTab plannedLibraryTab(String name, String note) =>
    (tabs, _) => PlannedLibraryTab(name: name, note: note, tabs: tabs);
