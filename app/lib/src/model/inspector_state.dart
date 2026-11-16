import 'package:flutter/foundation.dart';

import 'place.dart';

/// The inspector's three widths (f4-inspector.md decision 3).
enum InspectorWidth {
  /// A rail at the right edge: the column is closed, and one control opens
  /// it again.
  collapsed,

  /// The column beside the conversation, `Layout.inspector` wide.
  docked,

  /// The column takes the window, and the conversation narrows to an
  /// outline of step headlines with the composer still at its foot.
  expanded,
}

/// An agent thread's inspector tabs, in order (f4-inspector.md decision 2).
///
/// The set is the thread kind's: F6 adds the work item's and the plan run's,
/// which is why a widget reads the set rather than assuming these five.
enum InspectorTab { changes, files, terminal, preview, inspect }

/// The inspector's view state: how wide it is, which tab is showing, whether
/// that tab is pinned, and the node it is following.
///
/// It is a [ChangeNotifier] and imports nothing from Flutter beyond
/// `foundation.dart`, so its rules are plain unit tests (model/README.md).
/// The width and the tab are part of the [Place], so a link restores them.
class InspectorState extends ChangeNotifier {
  InspectorState({
    this.width = InspectorWidth.docked,
    this.tab = InspectorTab.changes,
    this.pinned = false,
    this.selectedNode,
  });

  /// The state a link names: the width and the tab in [place]'s query.
  factory InspectorState.fromPlace(Place place) => InspectorState(
    width: _widthNamed(place.query[linkWidthKey]) ?? InspectorWidth.docked,
    tab: _tabNamed(place.query[linkTabKey]) ?? InspectorTab.changes,
  );

  /// `w`: the width a link carries.
  static const widthKey = linkWidthKey;

  /// `t`: the tab a link carries.
  static const tabKey = linkTabKey;

  /// How wide the column is.
  InspectorWidth width;

  /// Which tab shows.
  InspectorTab tab;

  /// A pinned tab ignores selection changes: it stays where the operator put
  /// it while the agent works.
  bool pinned;

  /// The node the inspector is following, or null while it follows the live
  /// end.
  String? selectedNode;

  /// Whether it is following the live end.
  bool get live => selectedNode == null;

  /// The next width: collapsed to docked, docked to expanded, expanded back
  /// to collapsed. `⌘\` cycles them.
  void cycleWidth() => setWidth(switch (width) {
    InspectorWidth.collapsed => InspectorWidth.docked,
    InspectorWidth.docked => InspectorWidth.expanded,
    InspectorWidth.expanded => InspectorWidth.collapsed,
  });

  void setWidth(InspectorWidth width) {
    if (this.width == width) return;
    this.width = width;
    notifyListeners();
  }

  void chooseTab(InspectorTab tab) {
    if (this.tab == tab) return;
    this.tab = tab;
    notifyListeners();
  }

  /// Follows [nodeId], switching to Inspect. A pinned tab ignores this.
  ///
  /// Null is [backToLive]: selecting nothing means following the agent again.
  void select(String? nodeId) {
    if (pinned) return;
    if (nodeId == null) {
      backToLive();
      return;
    }
    if (selectedNode == nodeId && tab == InspectorTab.inspect) return;
    selectedNode = nodeId;
    tab = InspectorTab.inspect;
    notifyListeners();
  }

  /// Follows the live end again: no node selected, the tab unchanged.
  void backToLive() {
    if (selectedNode == null) return;
    selectedNode = null;
    notifyListeners();
  }

  void togglePinned() {
    pinned = !pinned;
    notifyListeners();
  }

  /// [place] with this state's width and tab in its query: what a link
  /// carries, and what [InspectorState.fromPlace] reads back.
  Place applyTo(Place place) =>
      place.withQuery({linkWidthKey: width.name, linkTabKey: tab.name});

  static InspectorWidth? _widthNamed(String? wire) {
    for (final width in InspectorWidth.values) {
      if (width.name == wire) return width;
    }
    return null;
  }

  static InspectorTab? _tabNamed(String? wire) {
    for (final tab in InspectorTab.values) {
      if (tab.name == wire) return tab;
    }
    return null;
  }
}
