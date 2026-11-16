import '../kit/kit.dart';

/// The tab bar Library draws through whichever section is open: the section
/// page draws its own tabs (design-system.md §5), so every tab carries the
/// bar and the index that is current.
@immutable
class LibraryTabs {
  const LibraryTabs({
    required this.items,
    required this.index,
    required this.onChanged,
  });

  final List<TabItem> items;
  final int index;
  final ValueChanged<int> onChanged;
}

/// One tab of Library: its content, given the bar it draws and the state the
/// shell knows.
typedef LibraryTab = Widget Function(LibraryTabs tabs, StateKind? state);

/// Library (F7 decision 5): Skills, Plugins, MCP and Memory, each one a
/// `SectionPage` with the same four tabs across the top.
class LibraryPlace extends StatefulWidget {
  const LibraryPlace({
    super.key,
    required this.skills,
    required this.plugins,
    required this.mcp,
    required this.memory,
    this.tab = 0,
    this.state,
  });

  final LibraryTab skills;
  final LibraryTab plugins;
  final LibraryTab mcp;
  final LibraryTab memory;

  /// The tab to open: Skills is 0, Plugins 1, MCP 2, Memory 3.
  final int tab;

  /// The state the shell knows, shown by the tab that is open.
  final StateKind? state;

  @override
  State<LibraryPlace> createState() => _LibraryPlaceState();
}

class _LibraryPlaceState extends State<LibraryPlace> {
  static const _labels = ['Skills', 'Plugins', 'MCP', 'Memory'];

  late int _tab = widget.tab < 0
      ? 0
      : (widget.tab >= _labels.length ? _labels.length - 1 : widget.tab);

  @override
  Widget build(BuildContext context) {
    final tabs = LibraryTabs(
      items: [for (final label in _labels) TabItem(label)],
      index: _tab,
      onChanged: (index) => setState(() => _tab = index),
    );
    final content = switch (_tab) {
      0 => widget.skills(tabs, widget.state),
      1 => widget.plugins(tabs, widget.state),
      2 => widget.mcp(tabs, widget.state),
      _ => widget.memory(tabs, widget.state),
    };
    // A tab is a page of its own: a fresh subtree per tab keeps one section's
    // open detail off the next.
    return KeyedSubtree(key: ValueKey(_tab), child: content);
  }
}
