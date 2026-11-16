import 'package:flutter/widgets.dart';

import '../tokens.dart';

/// What a pressable control is doing right now.
@immutable
class PressState {
  const PressState({
    required this.enabled,
    required this.hovered,
    required this.focused,
  });

  final bool enabled;
  final bool hovered;
  final bool focused;
}

/// The one way anything in the kit is clickable (design-system.md §9):
/// the pointer cursor, a hover the builder turns into a fill, keyboard focus
/// with one visible ring in `accent`, Enter and Space to activate, a button
/// in the semantics tree, and on the narrow layout a target of at least 44.
///
/// With no [onTap] the control is disabled: no cursor, no hover, no focus.
class Press extends StatefulWidget {
  const Press({
    super.key,
    required this.onTap,
    required this.builder,
    this.radius = Radii.control,
    this.semanticLabel,
    this.selected = false,
    this.ringInside = false,
  });

  final VoidCallback? onTap;

  /// Draws the control for its current state.
  final Widget Function(BuildContext context, PressState state) builder;

  /// The control's corner radius, which the focus ring follows.
  final double radius;

  /// The control's name for assistive technology, when its visible label
  /// is not enough (an icon-only control).
  final String? semanticLabel;

  /// Reported to assistive technology for a selected tab or row.
  final bool selected;

  /// Draw the focus ring just inside the control rather than just outside
  /// it: for a row that fills a clipped `Box`, where outside is cut off.
  final bool ringInside;

  @override
  State<Press> createState() => _PressState();
}

class _PressState extends State<Press> {
  bool _hovered = false;
  bool _focused = false;

  @override
  Widget build(BuildContext context) {
    final enabled = widget.onTap != null;
    final state = PressState(
      enabled: enabled,
      hovered: enabled && _hovered,
      focused: enabled && _focused,
    );
    final narrow = MediaQuery.sizeOf(context).width < Layout.narrow;

    Widget child = CustomPaint(
      foregroundPainter: state.focused
          ? _FocusRing(
              Ember.of(context).accent,
              widget.radius,
              inside: widget.ringInside,
            )
          : null,
      child: widget.builder(context, state),
    );
    if (narrow) {
      // §4: at least 44 on the narrow layout. The control keeps its drawn
      // size; the area that takes the tap grows around it.
      child = ConstrainedBox(
        constraints: const BoxConstraints(
          minWidth: Sizes.touch,
          minHeight: Sizes.touch,
        ),
        child: Align(widthFactor: 1, heightFactor: 1, child: child),
      );
    }

    return Semantics(
      button: true,
      enabled: enabled,
      selected: widget.selected,
      label: widget.semanticLabel,
      child: FocusableActionDetector(
        enabled: enabled,
        mouseCursor: enabled
            ? SystemMouseCursors.click
            : SystemMouseCursors.basic,
        onShowFocusHighlight: (value) => setState(() => _focused = value),
        actions: {
          ActivateIntent: CallbackAction<ActivateIntent>(
            onInvoke: (_) {
              widget.onTap?.call();
              return null;
            },
          ),
        },
        // Hover is the mouse's alone, whatever the focus highlight mode.
        child: MouseRegion(
          onEnter: (_) => setState(() => _hovered = true),
          onExit: (_) => setState(() => _hovered = false),
          child: GestureDetector(
            behavior: HitTestBehavior.opaque,
            onTap: widget.onTap,
            child: child,
          ),
        ),
      ),
    );
  }
}

/// A one-pixel `accent` outline, two pixels outside the control (or inside
/// it, for a row).
class _FocusRing extends CustomPainter {
  _FocusRing(this.color, this.radius, {required this.inside});

  final Color color;
  final double radius;
  final bool inside;

  @override
  void paint(Canvas canvas, Size size) {
    final rect = (Offset.zero & size).inflate(inside ? -Space.s2 : Space.s2);
    canvas.drawRRect(
      RRect.fromRectAndRadius(
        rect,
        Radius.circular(inside ? radius : radius + Space.s2),
      ),
      Paint()
        ..color = color
        ..style = PaintingStyle.stroke
        ..strokeWidth = Sizes.border,
    );
  }

  @override
  bool shouldRepaint(_FocusRing old) =>
      old.color != color || old.radius != radius || old.inside != inside;
}
