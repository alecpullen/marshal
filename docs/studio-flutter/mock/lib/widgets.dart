import 'package:cupertino_ui/cupertino_ui.dart' show CupertinoIcons;
import 'package:material_ui/material_ui.dart';

import 'data.dart';
import 'state.dart';
import 'theme.dart';

Color statusColor(S s, Status status) => switch (status) {
      Status.needsYou => s.warn,
      Status.running => s.running,
      Status.ready => s.ok,
      Status.failed => s.err,
      Status.idle => s.faint,
    };

String statusLabel(Status status) => switch (status) {
      Status.needsYou => 'Needs you',
      Status.running => 'Running',
      Status.ready => 'Ready',
      Status.failed => 'Failed',
      Status.idle => 'Idle',
    };

IconData kindIcon(Kind kind) => switch (kind) {
      Kind.agent => CupertinoIcons.chat_bubble,
      Kind.work => CupertinoIcons.square_stack_3d_up,
      Kind.run => CupertinoIcons.arrow_branch,
    };

String kindLabel(Kind kind) => switch (kind) {
      Kind.agent => 'Agent',
      Kind.work => 'Work item',
      Kind.run => 'Plan run',
    };

/// Rebuilds its child with a hover flag, and makes it tappable.
class Tap extends StatefulWidget {
  const Tap({super.key, required this.builder, this.onTap});
  final Widget Function(BuildContext context, bool hovered) builder;
  final VoidCallback? onTap;

  @override
  State<Tap> createState() => _TapState();
}

class _TapState extends State<Tap> {
  bool hovered = false;

  @override
  Widget build(BuildContext context) {
    return MouseRegion(
      cursor: widget.onTap == null ? MouseCursor.defer : SystemMouseCursors.click,
      onEnter: (_) => setState(() => hovered = true),
      onExit: (_) => setState(() => hovered = false),
      child: GestureDetector(
        behavior: HitTestBehavior.opaque,
        onTap: widget.onTap,
        child: widget.builder(context, hovered),
      ),
    );
  }
}

/// A bordered group. Borders, not background tint, are what group content.
class Box extends StatelessWidget {
  const Box({super.key, required this.child, this.padding = const EdgeInsets.all(14), this.onTap, this.edge});
  final Widget child;
  final EdgeInsets padding;
  final VoidCallback? onTap;
  final Color? edge;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    return Tap(
      onTap: onTap,
      builder: (context, hovered) => Container(
        padding: padding,
        decoration: BoxDecoration(
          color: hovered && onTap != null ? s.hover : null,
          border: Border.all(color: edge ?? s.border),
          borderRadius: BorderRadius.circular(s.radius),
        ),
        child: child,
      ),
    );
  }
}

/// A status dot. Running pulses; it is the only motion in the app.
class Dot extends StatefulWidget {
  const Dot(this.status, {super.key, this.size = 8, this.tooltip});
  final Status status;
  final String? tooltip;
  final double size;

  @override
  State<Dot> createState() => _DotState();
}

class _DotState extends State<Dot> with SingleTickerProviderStateMixin {
  late final AnimationController pulse =
      AnimationController(vsync: this, duration: const Duration(milliseconds: 1100));

  @override
  void initState() {
    super.initState();
    _sync();
  }

  @override
  void didUpdateWidget(Dot old) {
    super.didUpdateWidget(old);
    _sync();
  }

  void _sync() {
    if (widget.status == Status.running) {
      if (!pulse.isAnimating) pulse.repeat(reverse: true);
    } else {
      pulse.stop();
      pulse.value = 0;
    }
  }

  @override
  void dispose() {
    pulse.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final color = statusColor(context.s, widget.status);
    final reduce = MediaQuery.of(context).disableAnimations;
    return Tooltip(
      message: widget.tooltip ?? statusLabel(widget.status),
      child: AnimatedBuilder(
      animation: pulse,
      builder: (context, _) => Container(
        width: widget.size,
        height: widget.size,
        decoration: BoxDecoration(
          shape: BoxShape.circle,
          color: widget.status == Status.idle
              ? null
              : color.withValues(alpha: reduce ? 1 : 1 - pulse.value * 0.55),
          border: widget.status == Status.idle ? Border.all(color: color, width: 1.5) : null,
        ),
      ),
      ),
    );
  }
}

/// A small outlined label.
class Pill extends StatelessWidget {
  const Pill(this.label, {super.key, this.color, this.icon, this.mono = false, this.tooltip});
  final String? tooltip;
  final String label;
  final Color? color;
  final IconData? icon;
  final bool mono;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    final c = color ?? s.muted;
    final pill = _pill(s, c);
    return tooltip == null ? pill : Tooltip(message: tooltip!, child: pill);
  }

  Widget _pill(S s, Color c) {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 7, vertical: 2),
      decoration: BoxDecoration(
        border: Border.all(color: color == null ? s.border : c.withValues(alpha: 0.5)),
        borderRadius: BorderRadius.circular(999),
      ),
      child: Row(mainAxisSize: MainAxisSize.min, children: [
        if (icon != null) ...[Icon(icon, size: 11, color: c), const SizedBox(width: 4)],
        Flexible(
          child: Text(label,
              overflow: TextOverflow.ellipsis,
              style: mono ? s.mono(11, color: c) : s.text(11, color: c, weight: FontWeight.w500)),
        ),
      ]),
    );
  }
}

class Btn extends StatelessWidget {
  const Btn(this.label, {super.key, this.primary = false, this.icon, this.onTap, this.color});
  final String label;
  final bool primary;
  final IconData? icon;
  final VoidCallback? onTap;
  final Color? color;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    final fill = color ?? s.primary;
    return Tap(
      onTap: onTap ?? () {},
      builder: (context, hovered) => Container(
        padding: const EdgeInsets.symmetric(horizontal: 11, vertical: 6),
        decoration: BoxDecoration(
          color: primary ? fill.withValues(alpha: hovered ? 0.88 : 1) : (hovered ? s.hover : null),
          border: Border.all(color: primary ? fill : s.border),
          borderRadius: BorderRadius.circular(s.control),
        ),
        child: Row(mainAxisSize: MainAxisSize.min, children: [
          if (icon != null) ...[
            Icon(icon, size: 13, color: primary ? s.onPrimary : s.fg),
            const SizedBox(width: 6),
          ],
          Text(label, style: s.text(12.5, color: primary ? s.onPrimary : s.fg, weight: FontWeight.w500)),
        ]),
      ),
    );
  }
}

class IconBtn extends StatelessWidget {
  const IconBtn(this.icon, {super.key, this.onTap, this.tooltip, this.active = false});
  final IconData icon;
  final VoidCallback? onTap;
  final String? tooltip;
  final bool active;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    final button = Tap(
      onTap: onTap ?? () {},
      builder: (context, hovered) => Container(
        width: 28,
        height: 28,
        decoration: BoxDecoration(
          color: active ? s.selected : (hovered ? s.hover : null),
          borderRadius: BorderRadius.circular(s.control),
        ),
        child: Icon(icon, size: 15, color: active ? s.accent : s.muted),
      ),
    );
    return tooltip == null ? button : Tooltip(message: tooltip!, child: button);
  }
}

/// A small uppercase section label, optionally with a count.
class Label extends StatelessWidget {
  const Label(this.text, {super.key, this.count, this.color, this.trailing});
  final String text;
  final int? count;
  final Color? color;
  final Widget? trailing;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    return Row(children: [
      Text(text.toUpperCase(),
          style: s.text(11, color: color ?? s.muted, weight: FontWeight.w600).copyWith(letterSpacing: 0.6)),
      if (count != null) ...[
        const SizedBox(width: 6),
        Text('$count', style: s.mono(11, color: s.faint)),
      ],
      const Spacer(),
      ?trailing,
    ]);
  }
}

/// Progress as segments: done, one active, the rest pending.
class Segments extends StatelessWidget {
  const Segments({super.key, required this.done, required this.total, required this.status});
  final int done;
  final int total;
  final Status status;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    return Tooltip(
      message: '$done of $total steps done',
      child: Row(children: [
      for (var i = 0; i < total; i++)
        Expanded(
          child: Container(
            height: 3,
            margin: EdgeInsets.only(right: i == total - 1 ? 0 : 3),
            decoration: BoxDecoration(
              color: i < done
                  ? s.ok
                  : i == done
                      ? statusColor(s, status)
                      : s.border,
              borderRadius: BorderRadius.circular(2),
            ),
          ),
        ),
    ]),
    );
  }
}

class Spark extends StatelessWidget {
  const Spark(this.values, {super.key, this.color, this.height = 36});
  final List<double> values;
  final Color? color;
  final double height;

  @override
  Widget build(BuildContext context) => SizedBox(
        height: height,
        width: double.infinity,
        child: CustomPaint(painter: _SparkPainter(values, color ?? context.s.fg)),
      );
}

class _SparkPainter extends CustomPainter {
  _SparkPainter(this.values, this.color);
  final List<double> values;
  final Color color;

  @override
  void paint(Canvas canvas, Size size) {
    final max = values.reduce((a, b) => a > b ? a : b);
    final path = Path();
    for (var i = 0; i < values.length; i++) {
      final x = size.width * i / (values.length - 1);
      final y = size.height - 2 - (size.height - 4) * (values[i] / max);
      i == 0 ? path.moveTo(x, y) : path.lineTo(x, y);
    }
    canvas.drawPath(
      path,
      Paint()
        ..color = color
        ..style = PaintingStyle.stroke
        ..strokeWidth = 1.6
        ..strokeJoin = StrokeJoin.round,
    );
  }

  @override
  bool shouldRepaint(_SparkPainter old) => old.color != color || old.values != values;
}

/// A horizontal meter with a label and a value.
class Meter extends StatelessWidget {
  const Meter(this.label, this.value, this.fraction, {super.key, this.color});
  final String label;
  final String value;
  final double fraction;
  final Color? color;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    return Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
      Row(children: [
        Expanded(child: Text(label, style: s.text(12.5))),
        Text(value, style: s.mono(11.5, color: s.muted)),
      ]),
      const SizedBox(height: 6),
      ClipRRect(
        borderRadius: BorderRadius.circular(2),
        child: LinearProgressIndicator(
          value: fraction,
          minHeight: 3,
          backgroundColor: s.border,
          valueColor: AlwaysStoppedAnimation(color ?? s.muted),
        ),
      ),
    ]);
  }
}

/// Text tabs with an underline on the current one.
class Tabs extends StatelessWidget {
  const Tabs({super.key, required this.labels, required this.index, required this.onChanged, this.badges = const {}});
  final List<String> labels;
  final int index;
  final ValueChanged<int> onChanged;
  final Map<int, String> badges;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    return SingleChildScrollView(
      scrollDirection: Axis.horizontal,
      child: Row(children: [
        for (var i = 0; i < labels.length; i++)
          Tap(
            onTap: () => onChanged(i),
            builder: (context, hovered) => Container(
              margin: const EdgeInsets.only(right: 16),
              padding: const EdgeInsets.symmetric(vertical: 9),
              decoration: BoxDecoration(
                border: Border(
                  bottom: BorderSide(color: i == index ? s.accent : Colors.transparent, width: 2),
                ),
              ),
              child: Row(children: [
                Text(labels[i],
                    style: s.text(12.5,
                        color: i == index || hovered ? s.fg : s.muted,
                        weight: i == index ? FontWeight.w600 : FontWeight.w400)),
                if (badges[i] != null) ...[
                  const SizedBox(width: 5),
                  Text(badges[i]!, style: s.mono(11, color: s.faint)),
                ],
              ]),
            ),
          ),
      ]),
    );
  }
}

/// A page with a title row and scrolling content, centred at a readable width.
class PageBody extends StatelessWidget {
  const PageBody({super.key, required this.title, this.subtitle, this.actions = const [], required this.children, this.maxWidth = 1180});
  final String title;
  final String? subtitle;
  final List<Widget> actions;
  final List<Widget> children;
  final double maxWidth;

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    return SingleChildScrollView(
      padding: const EdgeInsets.fromLTRB(28, 22, 28, 40),
      child: Center(
        child: ConstrainedBox(
          constraints: BoxConstraints(maxWidth: maxWidth),
          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            Row(crossAxisAlignment: CrossAxisAlignment.center, children: [
              Expanded(
                child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                  Text(title, style: s.text(20, weight: FontWeight.w600)),
                  if (subtitle != null) ...[
                    const SizedBox(height: 3),
                    Text(subtitle!, style: s.text(13, color: s.muted)),
                  ],
                ]),
              ),
              for (final a in actions) Padding(padding: const EdgeInsets.only(left: 8), child: a),
            ]),
            const SizedBox(height: 20),
            ...children,
          ]),
        ),
      ),
    );
  }
}

class Hairline extends StatelessWidget {
  const Hairline({super.key, this.vertical = false});
  final bool vertical;

  @override
  Widget build(BuildContext context) => vertical
      ? Container(width: 1, color: context.s.border)
      : Container(height: 1, color: context.s.border);
}
