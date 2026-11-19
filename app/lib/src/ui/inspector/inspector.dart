import '../../model/inspector_state.dart';
import '../kit/kit.dart';

/// The inspector: the right-hand column of a thread (f4-inspector.md
/// decision 3, design-system.md §4).
///
/// It is the frame: the tabs, the width control, the pin, the line that says
/// which node it is following, and the body the host passes in for the tab
/// showing. It draws what it is given and never fetches.
///
/// It has three widths — [InspectorWidth.collapsed], a rail; `docked`, the
/// column at [Layout.inspector]; and `expanded`, which the host lays out to
/// take the window. `⌘\` cycles them, and so does the control in its header.
class Inspector extends StatelessWidget {
  const Inspector({
    super.key,
    required this.state,
    required this.tabs,
    required this.body,
    this.counts = const {},
    this.selection,
    this.onTab,
    this.onCycleWidth,
  });

  /// How wide the inspector is, which tab shows, whether that tab is pinned
  /// and the node it is following.
  final InspectorState state;

  /// The tabs this thread kind has, in order (decision 2): F6 adds the work
  /// item's and the plan run's sets.
  final List<InspectorTab> tabs;

  /// What a tab draws.
  final Widget Function(InspectorTab tab) body;

  /// The count beside a tab, for a tab that has one.
  final Map<InspectorTab, int> counts;

  /// The name of the node the inspector is following, or null while it
  /// follows the live end. The host resolves it: the frame does not read the
  /// transcript.
  final String? selection;

  /// Opens a tab, when the host wants to record it. Defaults to
  /// [InspectorState.chooseTab].
  final ValueChanged<InspectorTab>? onTab;

  /// Cycles the width, when the host wants to record it. Defaults to
  /// [InspectorState.cycleWidth].
  final VoidCallback? onCycleWidth;

  /// What a tab is called (§8: the product's nouns, sentence case).
  static String labelFor(InspectorTab tab) => switch (tab) {
    InspectorTab.changes => 'Changes',
    InspectorTab.files => 'Files',
    InspectorTab.terminal => 'Terminal',
    InspectorTab.preview => 'Preview',
    InspectorTab.inspect => 'Inspect',
    InspectorTab.plan => 'Plan',
    InspectorTab.agents => 'Agents',
    InspectorTab.checks => 'Checks',
    InspectorTab.pulls => 'Pull requests',
  };

  void _cycle() => (onCycleWidth ?? state.cycleWidth)();

  void _choose(InspectorTab tab) => (onTab ?? state.chooseTab)(tab);

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: state,
      builder: (context, _) => state.width == InspectorWidth.collapsed
          ? _rail(context)
          : _column(context),
    );
  }

  /// The closed inspector: one control that opens it again, so a collapsed
  /// column is never a dead end.
  Widget _rail(BuildContext context) => Align(
    alignment: Alignment.topCenter,
    child: Padding(
      padding: const EdgeInsets.only(top: Space.s8),
      child: IconBtn(
        Glyphs.chevron_left,
        tooltip: 'Show the inspector',
        onTap: _cycle,
      ),
    ),
  );

  Widget _column(BuildContext context) {
    final index = tabs.indexOf(state.tab);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(Space.s16, 0, Space.s8, 0),
          child: Row(
            children: [
              Expanded(
                child: Tabs(
                  tabs: [
                    for (final tab in tabs)
                      TabItem(labelFor(tab), count: counts[tab]),
                  ],
                  index: index < 0 ? 0 : index,
                  onChanged: (i) {
                    if (i >= 0 && i < tabs.length) _choose(tabs[i]);
                  },
                ),
              ),
              IconBtn(
                state.pinned ? Glyphs.pin_fill : Glyphs.pin,
                tooltip: state.pinned
                    ? 'Stop pinning this tab'
                    : 'Pin this tab, so selecting a step leaves it',
                onTap: state.togglePinned,
              ),
              IconBtn(
                state.width == InspectorWidth.expanded
                    ? Glyphs.arrow_down_right_arrow_up_left
                    : Glyphs.arrow_up_left_arrow_down_right,
                tooltip: state.width == InspectorWidth.expanded
                    ? 'Dock the inspector'
                    : 'Expand for review',
                onTap: _cycle,
              ),
            ],
          ),
        ),
        const Hairline(),
        if (selection != null) _following(context, selection!),
        Expanded(child: body(state.tab)),
      ],
    );
  }

  /// Which node the inspector is following, and the way back to the live end
  /// (decision 4).
  Widget _following(BuildContext context, String label) {
    final e = Ember.of(context);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(
            Space.s16,
            Space.s8,
            Space.s8,
            Space.s8,
          ),
          child: Row(
            children: [
              Expanded(
                child: Text(
                  'Following $label',
                  style: e.small.tint(e.muted),
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                ),
              ),
              Btn('Back to live', onTap: state.backToLive),
            ],
          ),
        ),
        const Hairline(),
      ],
    );
  }
}

/// A tab's state message, inset as the tab's content is.
///
/// A tab's ready state pads its list by [Space.s16]; a loading or failed
/// message drawn bare would touch the panel's edge instead, so every state
/// message a tab draws for itself goes through here (design-system.md §7). A
/// message inside a padded list is already inset and is not wrapped.
class TabMessage extends StatelessWidget {
  const TabMessage(this.child, {super.key});

  final Widget child;

  @override
  Widget build(BuildContext context) =>
      Padding(padding: const EdgeInsets.all(Space.s16), child: child);
}
