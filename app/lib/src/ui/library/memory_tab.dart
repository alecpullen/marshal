import '../../model/fleet_store.dart' show describeFailure;
import '../../model/library_model.dart';
import '../kit/kit.dart';
import '../sections/section_page.dart';
import '../automations/confirm_action.dart';
import 'library_place.dart' show LibraryTabs;

/// The Memory tab of Library (F7 decision 5): the project's memories with
/// their scope, provenance and confidence, and the suggestions to promote one
/// to a wider scope.
///
/// A promotion is an action the person approves, never something this section
/// does on its own.
class MemoryTab extends StatefulWidget {
  const MemoryTab({super.key, required this.model, this.tabs, this.state});

  final MemoryModel model;
  final LibraryTabs? tabs;
  final StateKind? state;

  @override
  State<MemoryTab> createState() => _MemoryTabState();
}

class _MemoryTabState extends State<MemoryTab> {
  /// The suggestion whose promotion is being confirmed.
  MemorySuggestion? _promoting;
  String? _error;
  var _busy = false;

  Future<void> _promote(MemorySuggestion suggestion) async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      await widget.model.promote(suggestion);
    } catch (error) {
      if (!mounted) return;
      setState(() {
        _busy = false;
        _error = describeFailure(error);
      });
      return;
    }
    if (!mounted) return;
    setState(() {
      _busy = false;
      _promoting = null;
    });
  }

  @override
  Widget build(BuildContext context) {
    return SectionPage<MemoryEntry>(
      title: 'Memory',
      subtitle: 'What this project has learned, and how sure it is',
      model: widget.model,
      tabs: widget.tabs?.items,
      tabIndex: widget.tabs?.index ?? 0,
      onTab: widget.tabs?.onChanged,
      state: widget.state,
      readOnly: true,
      header: _header(context),
      emptyMessage:
          'What an agent learns in this project is listed here, with where it '
          'learned it.',
      row: (entry, onTap) => RowItem(
        name: entry.content,
        description: _provenance(entry),
        value: entry.confidence,
        onTap: onTap,
      ),
      detail: (context, entry) =>
          _MemoryDetail(model: widget.model, entry: entry),
    );
  }

  /// The suggestions to promote, and the confirmation of the one being
  /// promoted. An empty list when there is nothing to suggest.
  List<Widget> _header(BuildContext context) {
    final e = Ember.of(context);
    final suggestions = widget.model.suggestions;
    if (suggestions.isEmpty) return const [];
    final promoting = _promoting;
    return [
      Label('Suggested promotions', count: suggestions.length),
      const SizedBox(height: Space.s8),
      Rows([
        for (final suggestion in suggestions)
          RowItem(
            name: _content(context, suggestion),
            description:
                'Also held in ${suggestion.matchProjectRoot} · promote it to '
                '${suggestion.suggestedScope}',
            value: 'Promote',
            onTap: _busy
                ? null
                : () => setState(() {
                    _promoting = suggestion;
                    _error = null;
                  }),
          ),
      ]),
      if (promoting != null) ...[
        const SizedBox(height: Space.s12),
        ConfirmAction(
          question: 'Promote this memory to ${promoting.suggestedScope}?',
          loss:
              'Every project that reads the wider scope sees it. The memory '
              'itself does not change.',
          confirmLabel: 'Promote it',
          onConfirm: () => _promote(promoting),
          onCancel: () => setState(() => _promoting = null),
        ),
      ],
      if (_error != null) ...[
        const SizedBox(height: Space.s12),
        Text(_error!, style: e.small.tint(e.err)),
      ],
    ];
  }

  /// The suggested memory's text, when the list holds it.
  String _content(BuildContext context, MemorySuggestion suggestion) {
    for (final entry in widget.model.items) {
      if (entry.id == suggestion.memoryID) return entry.content;
    }
    return 'Memory ${suggestion.memoryID}';
  }

  /// Where a memory came from: the agent and step that learned it, and the
  /// project it was learned in.
  static String _provenance(MemoryEntry entry) {
    final parts = <String>[
      if (entry.scope != null && entry.scope!.isNotEmpty) entry.scope!,
      if (entry.learnedAgent != null && entry.learnedAgent!.isNotEmpty)
        'learned by ${entry.learnedAgent}',
      if (entry.learnedStep != null && entry.learnedStep! > 0)
        'at step ${entry.learnedStep}',
      if (entry.learnedProjectRoot != null &&
          entry.learnedProjectRoot!.isNotEmpty)
        entry.learnedProjectRoot!,
    ];
    return parts.isEmpty ? entry.kind : parts.join(' · ');
  }
}

/// One memory's detail: what it says, where it came from, and the two things
/// a person does with it — say whether it still holds, or delete it.
class _MemoryDetail extends StatefulWidget {
  const _MemoryDetail({required this.model, required this.entry});

  final MemoryModel model;
  final MemoryEntry entry;

  @override
  State<_MemoryDetail> createState() => _MemoryDetailState();
}

class _MemoryDetailState extends State<_MemoryDetail> {
  var _confirming = false;
  var _busy = false;
  String? _note;
  String? _error;

  Future<void> _setConfidence(String confidence) async {
    setState(() {
      _busy = true;
      _note = null;
      _error = null;
    });
    try {
      await widget.model.setConfidence(widget.entry, confidence);
      if (!mounted) return;
      setState(() {
        _busy = false;
        _note = confidence == 'stale'
            ? 'Marked stale: agents stop relying on it.'
            : 'Confirmed: it still holds.';
      });
    } catch (error) {
      if (!mounted) return;
      setState(() {
        _busy = false;
        _error = describeFailure(error);
      });
    }
  }

  Future<void> _delete() async {
    setState(() => _error = null);
    try {
      await widget.model.delete(widget.entry);
    } catch (error) {
      if (!mounted) return;
      setState(() => _error = describeFailure(error));
      return;
    }
    if (!mounted) return;
    Navigator.of(context).maybePop();
  }

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final entry = widget.entry;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        _Fact(label: 'It says', value: entry.content),
        _Fact(label: 'Kind', value: entry.kind),
        _Fact(
          label: 'Scope',
          value: (entry.scope ?? '').isEmpty ? 'Not recorded' : entry.scope!,
        ),
        _Fact(label: 'Learned', value: _MemoryTabState._provenance(entry)),
        _Fact(label: 'Confidence', value: entry.confidence),
        if (_note != null) ...[
          const SizedBox(height: Space.s12),
          Text(_note!, style: e.small.tint(e.muted)),
        ],
        if (_error != null) ...[
          const SizedBox(height: Space.s12),
          Text(_error!, style: e.small.tint(e.err)),
        ],
        const SizedBox(height: Space.s20),
        Wrap(
          spacing: Space.s8,
          runSpacing: Space.s8,
          children: [
            Btn(
              'Confirm it',
              onTap: _busy ? null : () => _setConfidence('confirmed'),
            ),
            Btn(
              'Mark it stale',
              onTap: _busy ? null : () => _setConfidence('stale'),
            ),
          ],
        ),
        const SizedBox(height: Space.s20),
        if (_confirming)
          ConfirmAction(
            question: 'Delete this memory?',
            loss: 'Agents stop relying on it, and it is not recovered.',
            confirmLabel: 'Delete it',
            onConfirm: _delete,
            onCancel: () => setState(() => _confirming = false),
          )
        else
          Align(
            alignment: Alignment.centerLeft,
            child: Btn(
              'Delete this memory',
              onTap: () => setState(() => _confirming = true),
            ),
          ),
      ],
    );
  }
}

/// One labelled fact of a read-only detail.
class _Fact extends StatelessWidget {
  const _Fact({required this.label, required this.value});

  final String label;
  final String value;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Padding(
      padding: const EdgeInsets.only(bottom: Space.s8),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(label, style: e.small.tint(e.muted)),
          const SizedBox(height: Space.s2),
          Text(value, style: e.body),
        ],
      ),
    );
  }
}
