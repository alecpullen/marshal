import 'package:flutter/widgets.dart';
import 'package:material_ui/material_ui.dart'
    show InputDecoration, Material, MaterialType, TextField;

import '../tokens.dart';
import 'btn.dart';
import 'icon_btn.dart';
import 'pill.dart';
import 'press.dart';
import 'vocabulary.dart';

/// The one field a thread is driven from (design-system.md §5): the text, the
/// permission mode and the model, and the button that sends or steers.
///
/// While the agent is running the button steers and a Stop control stands
/// beside it; otherwise the button starts a turn. A session the user can only
/// read has no field at all: it has one line saying why.
class Composer extends StatelessWidget {
  const Composer({
    super.key,
    required this.controller,
    required this.hint,
    this.running = false,
    this.readOnly = false,
    this.readOnlyReason,
    this.mode = 'Ask',
    this.model,
    this.onSend,
    this.onStop,
    this.onMode,
    this.onModel,
  });

  final TextEditingController controller;

  /// What the field says when it is empty: "Reply to the agent…", "Steer the
  /// agent…".
  final String hint;

  /// Whether a turn is running, which makes the button steer and shows Stop.
  final bool running;

  /// Whether the session can only be read. The field is replaced by
  /// [readOnlyReason].
  final bool readOnly;

  /// Why the session is read-only, in words. Required when [readOnly].
  final String? readOnlyReason;

  /// The permission mode chip's label.
  final String mode;

  /// The model chip's label; no chip is drawn when it is null.
  final String? model;

  /// Sends the draft. Null disables the button — an empty draft, or a
  /// read-only session.
  final VoidCallback? onSend;

  /// Stops the running turn. Null while nothing runs.
  final VoidCallback? onStop;

  /// Opens the permission-mode menu.
  final VoidCallback? onMode;

  /// Opens the model menu.
  final VoidCallback? onModel;

  /// A chip that opens a menu when it has one: the kit's one clickable, so an
  /// actionable chip behaves like every other control.
  Widget _chip(BuildContext context, Widget pill, VoidCallback? onTap) {
    if (onTap == null) return pill;
    final e = Ember.of(context);
    return Press(
      onTap: onTap,
      radius: Radii.full,
      builder: (context, state) => DecoratedBox(
        decoration: BoxDecoration(
          color: state.hovered ? e.hover : null,
          borderRadius: BorderRadius.circular(Radii.full),
        ),
        child: pill,
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    if (readOnly) {
      return Semantics(
        readOnly: true,
        child: DecoratedBox(
          decoration: BoxDecoration(
            border: Border.all(color: e.border, width: Sizes.border),
            borderRadius: BorderRadius.circular(Radii.control),
          ),
          child: Padding(
            padding: const EdgeInsets.symmetric(
              horizontal: Space.s12,
              vertical: Space.s12,
            ),
            child: Row(
              children: [
                Icon(Glyphs.lock, size: Sizes.iconRow, color: e.muted),
                const SizedBox(width: Space.s8),
                Expanded(
                  child: Text(
                    readOnlyReason ?? 'This session can only be read.',
                    style: e.body.tint(e.muted),
                  ),
                ),
              ],
            ),
          ),
        ),
      );
    }
    final model = this.model;
    return DecoratedBox(
      decoration: BoxDecoration(
        border: Border.all(color: e.border, width: Sizes.border),
        borderRadius: BorderRadius.circular(Radii.panel),
      ),
      child: Padding(
        padding: const EdgeInsets.fromLTRB(
          Space.s12,
          Space.s4,
          Space.s8,
          Space.s8,
        ),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Material(
              type: MaterialType.transparency,
              child: TextField(
                controller: controller,
                minLines: 1,
                maxLines: 6,
                style: e.body,
                cursorColor: e.accent,
                onSubmitted: (_) => onSend?.call(),
                decoration: InputDecoration.collapsed(
                  hintText: hint,
                  hintStyle: e.body.tint(e.faint),
                ),
              ),
            ),
            const SizedBox(height: Space.s8),
            // Two groups, one row: the chips lead and the actions trail. The
            // outer wrap spends what is left of the row between them, so on a
            // narrow composer the actions take a run of their own rather than
            // squeezing the chips.
            Wrap(
              alignment: WrapAlignment.spaceBetween,
              runSpacing: Space.s6,
              crossAxisAlignment: WrapCrossAlignment.center,
              children: [
                Wrap(
                  spacing: Space.s8,
                  runSpacing: Space.s6,
                  crossAxisAlignment: WrapCrossAlignment.center,
                  children: [
                    _chip(
                      context,
                      ConstrainedBox(
                        constraints: const BoxConstraints(
                          maxWidth: Layout.sidebar,
                        ),
                        child: Pill.icon(
                          mode,
                          icon: Glyphs.shield,
                          tooltip: 'Permission mode',
                        ),
                      ),
                      onMode,
                    ),
                    if (model != null)
                      _chip(
                        context,
                        ConstrainedBox(
                          constraints: const BoxConstraints(
                            maxWidth: Layout.sidebar,
                          ),
                          child: Pill.icon(
                            model,
                            icon: Glyphs.sparkles,
                            tooltip: 'Model',
                            mono: true,
                          ),
                        ),
                        onModel,
                      ),
                    const IconBtn(
                      Glyphs.paperclip,
                      tooltip: 'Attach: not in this phase',
                    ),
                    const IconBtn(
                      Glyphs.mic,
                      tooltip: 'Dictate: not in this phase',
                    ),
                  ],
                ),
                Wrap(
                  spacing: Space.s8,
                  runSpacing: Space.s6,
                  crossAxisAlignment: WrapCrossAlignment.center,
                  children: [
                    if (running && onStop != null)
                      Btn('Stop', icon: Glyphs.stop, onTap: onStop),
                    Btn.primary(
                      running ? 'Steer' : 'Send',
                      icon: Glyphs.arrow_up,
                      onTap: onSend,
                    ),
                  ],
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }
}
