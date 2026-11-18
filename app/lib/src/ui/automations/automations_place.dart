import '../kit/kit.dart';

/// The tab bar Automations draws through whichever section is open: the
/// section page draws its own tabs (design-system.md §5), so every tab
/// carries the bar and the index that is current.
@immutable
class AutomationsTabs {
  const AutomationsTabs({
    required this.items,
    required this.index,
    required this.onChanged,
    this.trailing,
  });

  final List<TabItem> items;
  final int index;
  final ValueChanged<int> onChanged;

  /// The place's own control, drawn at the end of the tab row (F9 B4): the one
  /// New automation, beside the tabs whose kinds it can start.
  final Widget? trailing;
}

/// One tab of Automations: its content, given the bar it draws and the state
/// the shell knows.
typedef AutomationsTab = Widget Function(
  AutomationsTabs tabs,
  StateKind? state,
);

/// Automations (F7 decision 4): Bots, Schedules, Watches and Recipes, each one
/// a `SectionPage` with the same four tabs across the top.
///
/// Bots and Watches are planned until F7 plan 1's later steps land: a caller
/// hands their builders the planned row today and swaps them for the real tabs
/// when they exist, so this place does not change shape around them.
class AutomationsPlace extends StatefulWidget {
  const AutomationsPlace({
    super.key,
    required this.bots,
    required this.schedules,
    required this.watches,
    required this.recipes,
    this.tab = 0,
    this.state,
    this.counts = const {},
  });

  final AutomationsTab bots;
  final AutomationsTab schedules;
  final AutomationsTab watches;
  final AutomationsTab recipes;

  /// The tab to open: Bots is 0, Schedules 1, Watches 2, Recipes 3.
  final int tab;

  /// The state the shell knows, shown by the tab that is open.
  final StateKind? state;

  /// How many entries each tab has, by tab index (F9 B4). A tab with no count
  /// draws none, and so does a place the shell cannot count — the planned Bots
  /// tab of a fleet with no project.
  final Map<int, int?> counts;

  @override
  State<AutomationsPlace> createState() => _AutomationsPlaceState();
}

class _AutomationsPlaceState extends State<AutomationsPlace> {
  static const _labels = ['Bots', 'Schedules', 'Watches', 'Recipes'];

  late int _tab = widget.tab < 0
      ? 0
      : (widget.tab >= _labels.length ? _labels.length - 1 : widget.tab);

  @override
  Widget build(BuildContext context) {
    final tabs = AutomationsTabs(
      items: [
        for (var i = 0; i < _labels.length; i++)
          TabItem(_labels[i], count: widget.counts[i]),
      ],
      index: _tab,
      onChanged: (index) => setState(() => _tab = index),
    );
    final content = switch (_tab) {
      0 => widget.bots(tabs, widget.state),
      1 => widget.schedules(tabs, widget.state),
      2 => widget.watches(tabs, widget.state),
      _ => widget.recipes(tabs, widget.state),
    };
    // A tab is a page of its own: a fresh subtree per tab keeps one section's
    // open detail and half-typed edit off the next.
    return KeyedSubtree(key: ValueKey(_tab), child: content);
  }
}
