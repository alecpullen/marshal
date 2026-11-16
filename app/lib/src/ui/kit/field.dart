import 'package:flutter/widgets.dart';
import 'package:material_ui/material_ui.dart'
    show InputDecoration, Material, MaterialType, TextField;

import '../tokens.dart';

/// A text input (design-system.md §5): a border and nothing else, with its
/// label above it. The border is `accent` while it has focus and `err` when
/// [error] says what is wrong; the error is spelled out under it, so it is
/// never colour alone.
class Field extends StatefulWidget {
  const Field({
    super.key,
    required this.label,
    this.controller,
    this.hint,
    this.error,
    this.enabled = true,
    this.onChanged,
    this.onSubmitted,
  });

  final String label;
  final TextEditingController? controller;

  /// Placeholder text, in `faint`: an example, never an instruction the
  /// user must read.
  final String? hint;

  /// What is wrong with the value, in words.
  final String? error;
  final bool enabled;
  final ValueChanged<String>? onChanged;
  final ValueChanged<String>? onSubmitted;

  @override
  State<Field> createState() => _FieldState();
}

class _FieldState extends State<Field> {
  final _focus = FocusNode();

  @override
  void initState() {
    super.initState();
    _focus.addListener(() => setState(() {}));
  }

  @override
  void dispose() {
    _focus.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final error = widget.error;
    final edge = error != null
        ? e.err
        : _focus.hasFocus
        ? e.accent
        : e.border;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(widget.label, style: e.small.tint(e.muted)),
        const SizedBox(height: Space.s6),
        Container(
          constraints: const BoxConstraints(minHeight: Sizes.row),
          padding: const EdgeInsets.symmetric(horizontal: Space.s8),
          alignment: Alignment.centerLeft,
          decoration: BoxDecoration(
            border: Border.all(color: edge, width: Sizes.border),
            borderRadius: BorderRadius.circular(Radii.control),
          ),
          child: Material(
            type: MaterialType.transparency,
            child: TextField(
              controller: widget.controller,
              focusNode: _focus,
              enabled: widget.enabled,
              onChanged: widget.onChanged,
              onSubmitted: widget.onSubmitted,
              maxLines: 1,
              style: widget.enabled ? e.body : e.body.tint(e.muted),
              cursorColor: e.accent,
              decoration: InputDecoration.collapsed(
                hintText: widget.hint,
                hintStyle: e.body.tint(e.faint),
              ),
            ),
          ),
        ),
        if (error != null) ...[
          const SizedBox(height: Space.s4),
          Text(error, style: e.small.tint(e.err)),
        ],
      ],
    );
  }
}
