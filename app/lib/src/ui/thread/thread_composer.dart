import '../../model/composer_model.dart';
import '../../model/thread_view_state.dart';
import '../kit/kit.dart';

/// A thread's composer (design-system.md §5): the kit's `Composer`, bound to
/// the thread's draft and to [ComposerModel]'s routes.
///
/// The draft lives in the thread's [ThreadViewState], so switching threads
/// keeps each draft and a reconnect loses none of it.
class ThreadComposer extends StatefulWidget {
  const ThreadComposer({
    super.key,
    required this.model,
    required this.view,
    this.onMode,
    this.onModel,
  });

  final ComposerModel model;
  final ThreadViewState view;

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
    return AnimatedBuilder(
      animation: Listenable.merge([widget.model, widget.view]),
      builder: (context, _) {
        final model = widget.model;
        final failure = model.failure;
        return Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Composer(
              controller: _text,
              hint: model.running ? 'Steer the agent…' : 'Reply to the agent…',
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
            if (failure != null)
              Padding(
                padding: const EdgeInsets.only(top: Space.s4),
                child: Text(
                  failure,
                  style: Ember.of(context).small.tint(Ember.of(context).err),
                ),
              ),
          ],
        );
      },
    );
  }
}
