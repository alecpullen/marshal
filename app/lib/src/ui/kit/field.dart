import 'package:flutter/widgets.dart';
import 'package:material_ui/material_ui.dart'
    show InputDecoration, Material, MaterialType, TextField;

import '../tokens.dart';
import 'vocabulary.dart';

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
    this.mono = false,
  }) : _multiline = false,
       _search = false;

  /// A field for prose — a prompt, a body — that starts three lines tall and
  /// grows with what it holds.
  ///
  /// Set [mono] for text that is a document rather than prose — the
  /// environment designer's source — so its lines line up with the layers
  /// beside it (F7 decision 6).
  const Field.multiline({
    super.key,
    required this.label,
    this.controller,
    this.hint,
    this.error,
    this.enabled = true,
    this.onChanged,
    this.onSubmitted,
    this.mono = false,
  }) : _multiline = true,
       _search = false;

  /// The sidebar's search: no label above it, and a magnifying glass leads it.
  /// It is the one field without a label of its own, so the mark names it and
  /// its semantics label repeats the name (§9).
  const Field.search({
    super.key,
    this.controller,
    this.hint,
    this.enabled = true,
    this.onChanged,
    this.onSubmitted,
  }) : label = null,
       error = null,
       mono = false,
       _multiline = false,
       _search = true;

  /// The words above the input. Null in [Field.search] alone, which is led by
  /// its mark instead.
  final String? label;
  final TextEditingController? controller;

  /// Set the text in mono: for a document a person edits line by line, never
  /// for prose.
  final bool mono;

  /// Placeholder text, in `faint`: an example, never an instruction the
  /// user must read.
  final String? hint;

  /// What is wrong with the value, in words.
  final String? error;
  final bool enabled;
  final ValueChanged<String>? onChanged;
  final ValueChanged<String>? onSubmitted;

  final bool _multiline;
  final bool _search;

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
    final label = widget.label;
    final edge = error != null
        ? e.err
        : _focus.hasFocus
        ? e.accent
        : e.border;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        if (label != null) ...[
          Text(label, style: e.small.tint(e.muted)),
          const SizedBox(height: Space.s6),
        ],
        Container(
          constraints: BoxConstraints(
            minHeight: widget._multiline ? Sizes.touch : Sizes.row,
          ),
          padding: EdgeInsets.symmetric(
            horizontal: Space.s8,
            vertical: widget._multiline ? Space.s8 : 0,
          ),
          alignment: widget._multiline
              ? Alignment.topLeft
              : Alignment.centerLeft,
          decoration: BoxDecoration(
            border: Border.all(color: edge, width: Sizes.border),
            borderRadius: BorderRadius.circular(Radii.control),
          ),
          child: _input(e),
        ),
        if (error != null) ...[
          const SizedBox(height: Space.s4),
          Text(error, style: e.small.tint(e.err)),
        ],
      ],
    );
  }

  /// What the border holds: the input itself, led by a magnifying glass on the
  /// search variant, which has no label above it to name it.
  Widget _input(Ember e) {
    final input = Material(
      type: MaterialType.transparency,
      child: TextField(
        controller: widget.controller,
        focusNode: _focus,
        enabled: widget.enabled,
        onChanged: widget.onChanged,
        onSubmitted: widget.onSubmitted,
        minLines: widget._multiline ? 3 : null,
        maxLines: widget._multiline ? null : 1,
        style: !widget.enabled
            ? (widget.mono ? e.mono.tint(e.muted) : e.body.tint(e.muted))
            : (widget.mono ? e.mono : e.body),
        cursorColor: e.accent,
        decoration: InputDecoration.collapsed(
          hintText: widget.hint,
          hintStyle: e.body.tint(e.faint),
        ),
      ),
    );
    if (!widget._search) return input;
    return Semantics(
      label: 'Search',
      child: Row(
        children: [
          Icon(Glyphs.search, size: Sizes.iconRow, color: e.muted),
          const SizedBox(width: Space.s8),
          Expanded(child: input),
        ],
      ),
    );
  }
}
