import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'tip.dart';
import 'vocabulary.dart';

/// A status dot (design-system.md §5, §6): one of the five statuses, filled
/// in its colour, or hollow for idle. Running pulses — the only motion in
/// the app (§9) — and holds still when the system asks for reduced motion.
///
/// Its tooltip names the status; [tooltip] replaces that word with a fuller
/// one ("Worker running") when the context needs it.
class Dot extends StatefulWidget {
  const Dot(this.status, {super.key, this.tooltip});

  final Status status;
  final String? tooltip;

  /// The dot's diameter.
  static const double size = Space.s8;

  @override
  State<Dot> createState() => _DotState();
}

class _DotState extends State<Dot> with SingleTickerProviderStateMixin {
  late final AnimationController _pulse = AnimationController(
    vsync: this,
    duration: const Duration(milliseconds: 1100),
  );

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    _sync();
  }

  @override
  void didUpdateWidget(Dot old) {
    super.didUpdateWidget(old);
    _sync();
  }

  void _sync() {
    final still = MediaQuery.maybeDisableAnimationsOf(context) ?? false;
    if (widget.status == Status.running && !still) {
      if (!_pulse.isAnimating) _pulse.repeat(reverse: true);
    } else {
      _pulse
        ..stop()
        ..value = 0;
    }
  }

  @override
  void dispose() {
    _pulse.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final color = widget.status.color(e);
    final hollow = widget.status == Status.idle;
    return Tip(
      message: widget.tooltip ?? widget.status.word,
      child: Semantics(
        label: widget.tooltip ?? widget.status.word,
        child: FadeTransition(
          // 1 → 0.45 and back: a breath, not a blink.
          opacity: _pulse.drive(Tween(begin: 1, end: 0.45)),
          child: Container(
            width: Dot.size,
            height: Dot.size,
            decoration: BoxDecoration(
              shape: BoxShape.circle,
              color: hollow ? null : color,
              border: hollow
                  ? Border.all(color: color, width: Sizes.border)
                  : null,
            ),
          ),
        ),
      ),
    );
  }
}
