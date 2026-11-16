import 'dart:async';
import 'package:flutter/rendering.dart' show SelectedContent;
import 'package:material_ui/material_ui.dart';

/// Deterministic variable-height row text.
String rowText(int i) {
  final words = 3 + (i * 7) % 40;
  return 'R$i ${List.generate(words, (w) => 'w${i}_$w').join(' ')}';
}

enum Approach { listBuilder, listBuilderNewline, keepAlive, perTurn }

void main() => runApp(const MaterialApp(home: Spike(approach: Approach.listBuilderNewline)));

class Spike extends StatefulWidget {
  const Spike({super.key, required this.approach, this.rows = 5000, this.onSelection, this.appendEvery, this.controller});
  final Approach approach;
  final int rows;
  final ValueChanged<SelectedContent?>? onSelection;
  final Duration? appendEvery;
  final ScrollController? controller;
  @override
  State<Spike> createState() => SpikeState();
}

class SpikeState extends State<Spike> {
  late int count = widget.rows;
  Timer? _t;
  int builtMax = 0;
  final Set<int> built = {};

  @override
  void initState() {
    super.initState();
    if (widget.appendEvery != null) {
      _t = Timer.periodic(widget.appendEvery!, (_) => append());
    }
  }

  void append() => setState(() => count++);

  @override
  void dispose() {
    _t?.cancel();
    super.dispose();
  }

  Widget row(int i) {
    final nl = widget.approach != Approach.listBuilder;
    final Widget w = Padding(
      padding: const EdgeInsets.symmetric(vertical: 4, horizontal: 12),
      child: nl
          // Trailing near-zero-size newline so plain text of adjacent rows is
          // separated by \n without adding visible height.
          ? Text.rich(TextSpan(children: [TextSpan(text: rowText(i)), const TextSpan(text: '\n', style: TextStyle(fontSize: 0.01, height: 0.01))]), key: ValueKey('row$i'))
          : Text(rowText(i), key: ValueKey('row$i')),
    );
    return widget.approach == Approach.keepAlive ? Keep(child: w) : w;
  }

  // One Text.rich per turn of 10 rows (lines joined by newline).
  Widget turn(int t) {
    final spans = <InlineSpan>[
      for (var i = t * 10; i < t * 10 + 10 && i < count; i++) TextSpan(text: '${rowText(i)}\n'),
    ];
    return Padding(
      padding: const EdgeInsets.all(12),
      child: Text.rich(TextSpan(children: spans), key: ValueKey('turn$t')),
    );
  }

  @override
  Widget build(BuildContext context) {
    final builder = widget.approach != Approach.perTurn
        ? ListView.builder(
            controller: widget.controller,
            itemCount: count,
            itemBuilder: (c, i) {
              built.add(i);
              return row(i);
            },
          )
        : ListView.builder(
            controller: widget.controller,
            itemCount: (count + 9) ~/ 10,
            itemBuilder: (c, i) {
              built.add(i);
              return turn(i);
            },
          );
    return Scaffold(
      body: SelectionArea(onSelectionChanged: widget.onSelection, child: builder),
    );
  }
}

class Keep extends StatefulWidget {
  const Keep({super.key, required this.child});
  final Widget child;
  @override
  State<Keep> createState() => _KeepState();
}

class _KeepState extends State<Keep> with AutomaticKeepAliveClientMixin {
  @override
  bool get wantKeepAlive => true;
  @override
  Widget build(BuildContext context) {
    super.build(context);
    return widget.child;
  }
}
