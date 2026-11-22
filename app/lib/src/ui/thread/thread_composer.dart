import '../../model/composer_model.dart';
import '../../model/thread_view_state.dart';
import '../kit/kit.dart';

/// A thread's composer (design-system.md §5): the kit's `Composer`, bound to
/// the thread's draft and to [ComposerModel]'s routes.
///
/// The draft lives in the thread's [ThreadViewState], so switching threads
/// keeps each draft and a reconnect loses none of it.
///
/// It names the recipient of the conversation above the field when the host
/// gives one — the project's agent here, **Marshal** for a coordinator thread
/// (agent-first Studio, task 5) — so a person reading a transcript always knows
/// who the next turn is addressed to. It is a parameter rather than a fact
/// derived here: this widget draws, and who a thread belongs to is the host's.
class ThreadComposer extends StatefulWidget {
  const ThreadComposer({
    super.key,
    required this.model,
    required this.view,
    this.recipient,
    this.onMode,
    this.onModel,
  });

  final ComposerModel model;
  final ThreadViewState view;

  /// Who the next turn is addressed to, named above the field. Null draws no
  /// name, which is what a host that does not know the recipient leaves.
  final String? recipient;

  /// Opens the permission-mode menu. The kit's composer posts the chosen mode
  /// through [ComposerModel.selectMode].
  final VoidCallback? onMode;

  /// Opens the model menu.
  final VoidCallback? onModel;

  @override
  State<ThreadComposer> createState() => _ThreadComposerState();
}

class _ThreadComposerState extends State<ThreadComposer> {
  late final TextEditingController _text = TextEditingController(
    text: widget.view.draft,
  );

  @override
  void initState() {
    super.initState();
    widget.view.addListener(_syncFromThread);
    _text.addListener(_syncToThread);
  }

  @override
  void didUpdateWidget(ThreadComposer old) {
    super.didUpdateWidget(old);
    if (old.view != widget.view) {
      old.view.removeListener(_syncFromThread);
      widget.view.addListener(_syncFromThread);
      _syncFromThread();
    }
  }

  @override
  void dispose() {
    widget.view.removeListener(_syncFromThread);
    _text
      ..removeListener(_syncToThread)
      ..dispose();
    super.dispose();
  }

  /// The composer's text follows the thread's draft (a clear after a send, a
  /// draft restored by another screen).
  void _syncFromThread() {
    if (_text.text == widget.view.draft) return;
    _text.text = widget.view.draft;
  }

  void _syncToThread() {
    if (widget.view.draft == _text.text) return;
    widget.view.draft = _text.text;
  }

  @override
  Widget build(BuildContext context) {
    // An on-screen keyboard takes the bottom of the window, so the composer
    // pads itself by that inset: the field and Send stay visible above it on a
    // phone-sized viewport.
    return Padding(
      padding: EdgeInsets.only(bottom: MediaQuery.viewInsetsOf(context).bottom),
      child: AnimatedBuilder(
        animation: Listenable.merge([widget.model, widget.view]),
        builder: (context, _) {
          final e = Ember.of(context);
          final model = widget.model;
          final failure = model.failure;
          final recipient = widget.recipient;
          return Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              // The recipient is named above the field, so a thread that can
              // address more than one agent — a project agent here, Marshal in
              // a coordinator thread — never leaves the next turn's reader
              // guessing who it is for.
              if (recipient != null)
                Padding(
                  padding: const EdgeInsets.only(bottom: Space.s4),
                  child: Text(
                    recipient,
                    style: e.small.tint(e.muted),
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                  ),
                ),
              Composer(
                controller: _text,
                hint: model.running
                    ? 'Steer the agent…'
                    : 'Reply to the agent…',
                running: model.running,
                readOnly: model.readOnly,
                readOnlyReason: model.readOnlyReason,
                mode: model.permissionMode,
                model: model.model,
                onSend: model.readOnly ? null : () => model.send(_text.text),
                onStop: model.running ? model.stop : null,
                onMode: widget.onMode,
                onModel: widget.onModel,
              ),
              // A stop is a request: the session has not said the turn ended
              // yet, so the composer says what it is waiting for rather than
              // claiming the turn stopped.
              if (model.cancellationRequested && model.running)
                Padding(
                  padding: const EdgeInsets.only(top: Space.s4),
                  child: Text(
                    'Cancellation requested — waiting for the session to stop.',
                    style: e.small.tint(e.muted),
                  ),
                ),
              if (failure != null)
                Padding(
                  padding: const EdgeInsets.only(top: Space.s4),
                  child: Text(failure, style: e.small.tint(e.err)),
                ),
            ],
          );
        },
      ),
    );
  }
}
