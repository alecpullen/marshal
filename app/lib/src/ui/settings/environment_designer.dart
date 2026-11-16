import '../../model/environment_model.dart';
import '../../model/environments_model.dart' show BuildLog;
import '../../model/section_model.dart' show SaveState;
import '../kit/kit.dart';
import '../sections/detail_host.dart';
import 'layer_list.dart';

/// The environment designer (F7 decision 6): the layers on the left, the
/// source on the right, and one document behind both — stacked, with a switch
/// between the two views, below 1080.
///
/// The source is a monospace [Field] the bridge validates on save (the plan's
/// decision): there is no code editor, and a diagnostic the bridge reports is
/// shown beside the panel while the edit stays.
class EnvironmentDesigner extends StatefulWidget {
  const EnvironmentDesigner({super.key, required this.model, this.state});

  final EnvironmentModel model;

  /// The §7 state, for the screen registry: it replaces the panes.
  final StateKind? state;

  @override
  State<EnvironmentDesigner> createState() => _EnvironmentDesignerState();
}

class _EnvironmentDesignerState extends State<EnvironmentDesigner> {
  late final _source = TextEditingController(text: widget.model.source);
  LayerKind? _lastSelected;
  BuildLog? _log;
  var _tab = 0;

  @override
  void initState() {
    super.initState();
    widget.model.addListener(_sync);
    _sync();
  }

  @override
  void dispose() {
    widget.model.removeListener(_sync);
    _source.dispose();
    _log?.dispose();
    super.dispose();
  }

  /// Keeps the field and the model in step: the model's source is the
  /// authority, and a chosen layer moves the field's selection onto the
  /// layer's range.
  void _sync() {
    final model = widget.model;
    if (_source.text != model.source) {
      _source.value = TextEditingValue(text: model.source);
    }
    final selected = model.selected;
    if (selected != _lastSelected) {
      _lastSelected = selected;
      if (selected != null) {
        final (start, end) = model.charRangeOf(selected);
        _source.selection = TextSelection(baseOffset: start, extentOffset: end);
      }
    }
    if (mounted) setState(() {});
  }

  /// Saves the draft; the panel shows whatever the bridge said about it.
  Future<void> _save() async {
    await widget.model.saveDraft();
  }

  /// Starts a build and shows its log from the first line.
  Future<void> _build() async {
    try {
      final n = await widget.model.build();
      if (!mounted) return;
      setState(() => _log = widget.model.openBuildLog(n));
    } catch (error) {
      if (!mounted) return;
      setState(() => _log = null);
    }
  }

  /// Opens one layer's own text in a sheet, and writes it back on Apply: the
  /// rest of the source keeps its bytes.
  Future<void> _editLayer(LayerKind kind) async {
    final text = TextEditingController(text: widget.model.textOf(kind));
    await Sheet.show<void>(
      context,
      Sheet(
        title: 'Edit ${kind.label}',
        actions: [Btn('Cancel', onTap: () => Navigator.maybePop(context))],
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Field.multiline(
              label:
                  'Lines ${widget.model.layerOf(kind)?.sourceStart ?? 0}'
                  '–${widget.model.layerOf(kind)?.sourceEnd ?? 0}',
              controller: text,
              mono: true,
            ),
            const SizedBox(height: Space.s16),
            Align(
              alignment: Alignment.centerLeft,
              child: Btn.primary(
                'Apply',
                onTap: () {
                  widget.model.editLayer(kind, text.text);
                  Navigator.maybePop(context);
                },
              ),
            ),
          ],
        ),
      ),
    );
    text.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final state = widget.state;
    if (state != null) return _stateBody(state);
    final narrow = MediaQuery.sizeOf(context).width < Layout.medium;
    final source = _sourceBody();
    // A pane that scrolls rather than overflows: the layer list, the source and
    // the panel each grow with their content, and text scaled up past the
    // window is the reader's, not a broken layout (§4).
    if (!narrow) {
      return SingleChildScrollView(
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            SizedBox(width: Layout.sidebar, child: _layersBody()),
            const SizedBox(width: Space.s16),
            Expanded(child: source),
            const SizedBox(width: Space.s16),
            SizedBox(width: Layout.sidebar, child: _panelBody()),
          ],
        ),
      );
    }
    return SingleChildScrollView(
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Tabs(
            tabs: const [TabItem('Layers'), TabItem('Source')],
            index: _tab,
            onChanged: (index) => setState(() => _tab = index),
          ),
          const SizedBox(height: Space.s16),
          if (_tab == 0) _layersBody() else source,
          const SizedBox(height: Space.s20),
          _panelBody(),
        ],
      ),
    );
  }

  /// One §7 state, in place of the panes.
  Widget _stateBody(StateKind kind) {
    final retry = StateAction(
      'Read the template again',
      onTap: widget.model.load,
    );
    return switch (kind) {
      StateKind.loading => const StateMessage.loading(lines: 6),
      StateKind.empty => StateMessage.empty(
        message: 'This environment has no source yet.',
        action: retry,
      ),
      StateKind.error => StateMessage.error(
        message: widget.model.saveError ?? 'The template could not be read.',
        retry: retry,
      ),
      StateKind.unavailable => StateMessage.unavailable(
        host: 'The daemon',
        lastSeen: 'a minute',
        action: retry,
      ),
      StateKind.stale => const StateMessage.stale(age: 'a minute'),
      StateKind.planned => StateMessage.planned(
        name: 'The designer',
        note: 'Not built yet',
      ),
    };
  }

  Widget _layersBody() {
    final model = widget.model;
    final selected = model.selected;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        LayerList(model: model),
        if (selected != null) ...[
          const SizedBox(height: Space.s12),
          Align(
            alignment: Alignment.centerLeft,
            child: Btn('Edit this layer', onTap: () => _editLayer(selected)),
          ),
        ],
      ],
    );
  }

  Widget _sourceBody() {
    return Field.multiline(
      label: 'Source',
      controller: _source,
      mono: true,
      onChanged: widget.model.editSource,
    );
  }

  Widget _panelBody() {
    final e = Ember.of(context);
    final model = widget.model;
    final log = _log;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        for (final (term, meaning) in model.panelFacts) ...[
          Text(term, style: e.small.tint(e.muted)),
          const SizedBox(height: Space.s2),
          Text(meaning, style: e.body),
          const SizedBox(height: Space.s12),
        ],
        if (model.diagnostics.isNotEmpty) ...[
          for (final diagnostic in model.diagnostics)
            Padding(
              padding: const EdgeInsets.only(bottom: Space.s4),
              child: Text(
                'Line ${diagnostic.line}: ${diagnostic.message}',
                style: e.small.tint(
                  diagnostic.severity == 'error' ? e.err : e.warn,
                ),
              ),
            ),
          const SizedBox(height: Space.s12),
        ],
        if (model.saveError != null) ...[
          Text(model.saveError!, style: e.body.tint(e.err)),
          const SizedBox(height: Space.s12),
        ],
        Wrap(
          spacing: Space.s8,
          runSpacing: Space.s8,
          children: [
            Btn(
              model.saveState == SaveState.saving ? 'Saving' : 'Save draft',
              onTap:
                  model.hasUnsavedEdits && model.saveState != SaveState.saving
                  ? _save
                  : null,
            ),
            Btn(model.building ? 'Building' : 'Build', onTap: _build),
            Btn.primary(
              'Publish',
              onTap: model.canPublish ? () => model.publish() : null,
            ),
          ],
        ),
        if (log != null) ...[
          const SizedBox(height: Space.s16),
          AnimatedBuilder(
            animation: log,
            builder: (context, _) => OutputBlock(text: log.lines.join('\n')),
          ),
        ],
      ],
    );
  }
}

/// Opens the designer for one template, in a sheet on a wide window and as a
/// pushed page below (the section's own detail host).
Future<void> openDesigner(
  BuildContext context, {
  required String title,
  required EnvironmentModel model,
}) => openDetail(
  context,
  title: title,
  body: SingleChildScrollView(
    child: EnvironmentDesigner(key: ValueKey(model.name), model: model),
  ),
);
