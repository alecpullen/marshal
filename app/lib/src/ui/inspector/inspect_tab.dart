import 'dart:async';

import '../../model/inspect_model.dart';
import '../kit/kit.dart';

/// The Inspect tab (f4-inspector.md decision 11): the selected node's actor,
/// why, timing, sandbox, approval and call id, then its output, arguments,
/// thinking and the last model request.
///
/// The model request is read on demand: it is the largest thing the bridge
/// can answer, so it is asked for when its tab is opened and not before.
class InspectTab extends StatefulWidget {
  const InspectTab({super.key, required this.model, this.state});

  final InspectModel model;

  /// The data state to draw instead of the node (§7). Null is the ready
  /// state.
  final StateKind? state;

  @override
  State<InspectTab> createState() => _InspectTabState();
}

class _InspectTabState extends State<InspectTab> {
  static const _tabs = ['Output', 'Arguments', 'Thinking', 'Model request'];
  int _tab = 0;

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: widget.model,
      builder: (context, _) {
        final state = widget.state;
        return state == null ? _body(context) : _stateOnly(state);
      },
    );
  }

  /// The five states every screen that shows data has (§7).
  Widget _stateOnly(StateKind state) => switch (state) {
    StateKind.loading => const StateMessage.loading(lines: 5),
    StateKind.empty => StateMessage.empty(
      message: 'Select a step to inspect it.',
      action: StateAction('Back to live', onTap: () => setState(() {})),
    ),
    StateKind.stale => const StateMessage.stale(
      age: '4m',
      detail: 'This detail may be behind',
    ),
    StateKind.error => StateMessage.error(
      message: 'The step could not be read.',
      retry: StateAction('Try again', onTap: () => setState(() {})),
    ),
    StateKind.unauthorized => const StateMessage.unauthorized(),
    StateKind.unavailable => StateMessage.unavailable(
      host: 'The bridge',
      lastSeen: '4m',
      action: StateAction('Try again', onTap: () => setState(() {})),
    ),
    StateKind.planned => const StateMessage.planned(
      name: 'A step’s own diff',
      note: 'The Inspector shows the change set’s diff',
    ),
  };

  Widget _body(BuildContext context) {
    final e = Ember.of(context);
    final facts = widget.model.facts;
    if (widget.model.nodeId == null) {
      return StateMessage.empty(
        message: 'Select a step to inspect it.',
        action: StateAction('Back to live', onTap: () => setState(() {})),
      );
    }
    if (widget.model.loading) return const StateMessage.loading(lines: 5);
    final error = widget.model.error;
    if (error != null) {
      return StateMessage.error(
        message: error,
        retry: StateAction(
          'Try again',
          onTap: () => unawaited(widget.model.select(widget.model.nodeId)),
        ),
      );
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Expanded(
          child: ListView(
            padding: const EdgeInsets.all(Space.s16),
            children: [
              Text(facts.title, style: e.bodyStrong),
              const SizedBox(height: Space.s8),
              Rows([
                if (facts.actor != null)
                  RowItem(name: 'Actor', description: facts.actor!),
                if (facts.why != null)
                  RowItem(name: 'Why', description: facts.why!),
                if (facts.took != null)
                  RowItem(name: 'Took', description: facts.took!, mono: true),
                if (facts.sandbox != null)
                  RowItem(
                    name: 'Sandbox',
                    description: facts.sandbox!,
                    mono: true,
                  ),
                if (facts.approval != null && facts.approval!.isNotEmpty)
                  RowItem(name: 'Approval', description: facts.approval!),
                if (facts.callId != null)
                  RowItem(name: 'Call', description: facts.callId!, mono: true),
              ]),
              const SizedBox(height: Space.s16),
              Tabs(
                tabs: [for (final label in _tabs) TabItem(label)],
                index: _tab,
                onChanged: (index) {
                  setState(() => _tab = index);
                  if (index == 3) unawaited(widget.model.loadRequest());
                },
              ),
              const Hairline(),
              const SizedBox(height: Space.s8),
              _panel(context, facts),
            ],
          ),
        ),
      ],
    );
  }

  Widget _panel(BuildContext context, InspectFacts facts) {
    final e = Ember.of(context);
    switch (_tab) {
      case 0:
        return facts.output.isEmpty
            ? Text('No output recorded.', style: e.small.tint(e.muted))
            : OutputBlock(text: facts.output);
      case 1:
        return facts.arguments.isEmpty
            ? Text('This call had no arguments.', style: e.small.tint(e.muted))
            : OutputBlock(text: facts.arguments);
      case 2:
        if (facts.thinking.isEmpty) {
          return Text('No thinking recorded.', style: e.small.tint(e.muted));
        }
        return Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            for (final thought in facts.thinking)
              Padding(
                padding: const EdgeInsets.only(bottom: Space.s8),
                child: Text(thought, style: e.small),
              ),
          ],
        );
      default:
        final request = widget.model.requestLoading
            ? 'Reading the request…'
            : widget.model.modelRequest;
        if (request == null) {
          return Text(
            'Open this tab to read the request.',
            style: e.small.tint(e.muted),
          );
        }
        return OutputBlock(text: request);
    }
  }
}
