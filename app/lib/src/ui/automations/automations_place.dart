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
    this.onNew,
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

  /// Starts one kind of automation from the place's one New (F9 B4): tab 1
  /// opens a schedule's form, tab 2 the watch form. A recipe is made by
  /// copying a built-in, so tab 3 only opens the Recipes tab.
  final Future<void> Function(int tab, BuildContext context)? onNew;

  @override
  State<AutomationsPlace> createState() => _AutomationsPlaceState();
}

class _AutomationsPlaceState extends State<AutomationsPlace> {
  static const _labels = ['Bots', 'Schedules', 'Watches', 'Recipes'];

  late int _tab = widget.tab < 0
      ? 0
      : (widget.tab >= _labels.length ? _labels.length - 1 : widget.tab);

  /// The tab bar's own change: a tap on a tab, and the Recipe choice below,
  /// move the place the same way.
  void _select(int index) => setState(() => _tab = index);

  @override
  Widget build(BuildContext context) {
    // Schedules (1) and Watches (2) each draw their own New in the page
    // header, so the place's one New (F9 B4) belongs only to the tabs that
    // have no create of their own: Bots and Recipes.
    final hasOwnNew = _tab == 1 || _tab == 2;
    final tabs = AutomationsTabs(
      items: [
        for (var i = 0; i < _labels.length; i++)
          TabItem(_labels[i], count: widget.counts[i]),
      ],
      index: _tab,
      onChanged: _select,
      // The place's one New (F9 B4), on the tabs with no create of their
      // own: Bots is set per project and a recipe is made by copying a
      // built-in, which the Recipes tab holds, so neither tab has a New.
      trailing: hasOwnNew
          ? null
          : Menu(
              choices: [
                MenuChoice(
                  'Schedule',
                  'A cron entry that runs a recipe',
                  onSelect: () => widget.onNew?.call(1, context),
                ),
                MenuChoice(
                  'Watch',
                  'Watch a command, a job or a file',
                  onSelect: () => widget.onNew?.call(2, context),
                ),
                MenuChoice(
                  'Recipe',
                  'Open the recipes to copy one',
                  onSelect: () => _select(3),
                ),
              ],
              anchor: (context, toggle) =>
                  Btn('New automation', icon: Glyphs.plus, onTap: toggle),
            ),
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
