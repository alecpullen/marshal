/// The kit: everything a screen under `lib/src/ui/` is built from
/// (design-system.md §5), in one import.
///
/// A screen imports this and nothing from Material or Cupertino
/// (`test/boundary_test.dart`, `test/design_rules_test.dart`). It brings:
///
/// - the components of §5, one file each;
/// - the supporting pieces they share: `EmberApp`, the root; `Tip`, the one
///   tooltip; the §6 vocabulary (`Status`, `ThreadKind`, `CheckState`,
///   `Check`) and `Glyphs`, the one icon set;
/// - the tokens;
/// - Flutter's widgets layer, without the pieces a screen would only use to
///   style something by hand — decorations, borders, radii, text styles,
///   bare containers and boxes that paint. Those belong to the kit; a screen
///   that needs one has found a gap in the kit (§10).
library;

export 'package:flutter/widgets.dart'
    hide
        Border,
        BorderRadius,
        BorderSide,
        BoxDecoration,
        BoxShadow,
        ColoredBox,
        Container,
        DecoratedBox,
        DefaultTextStyle,
        Radius,
        ShapeDecoration,
        TextStyle;

export '../tokens.dart';
export 'activity_row.dart';
export 'ask_card.dart';
export 'box.dart';
export 'btn.dart';
export 'chart.dart';
export 'check_strip.dart';
export 'composer.dart';
export 'diff_view.dart';
export 'dot.dart';
export 'ember_app.dart';
export 'field.dart';
export 'gate_list.dart';
export 'hairline.dart';
export 'icon_btn.dart';
export 'label.dart';
export 'lanes.dart';
export 'live_row.dart';
export 'menu.dart';
export 'nav_row.dart';
export 'meter.dart';
export 'narration.dart';
export 'notice.dart';
export 'output_block.dart';
export 'page_body.dart';
export 'pill.dart';
export 'pr_card.dart';
export 'rows.dart';
export 'segments.dart';
export 'sheet.dart';
export 'spark.dart';
export 'stage_strip.dart';
export 'state_message.dart';
export 'step_header.dart';
export 'tabs.dart';
export 'task_graph.dart';
export 'thread_row.dart';
export 'tip.dart';
export 'tool_row.dart';
export 'turn_prompt.dart';
export 'vocabulary.dart';
