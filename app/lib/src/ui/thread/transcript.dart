import '../../model/age.dart';
import '../../model/inbox_model.dart' as model;
import '../../model/place.dart';
import '../../model/selection.dart';
import '../../model/stack_store.dart';
import '../../model/thread_view_state.dart';
import '../inbox/answerable_ask.dart';
import '../kit/kit.dart';
import 'selection_layer.dart';

/// One row of the flattened transcript: a node, and how deep it sits.
@immutable
class TranscriptRow {
  const TranscriptRow(this.id, this.depth);

  /// The node this row draws.
  final String id;

  /// 0 for a turn, 1 for what it holds.
  final int depth;

  @override
  bool operator ==(Object other) =>
      other is TranscriptRow && other.id == id && other.depth == depth;

  @override
  int get hashCode => Object.hash(id, depth);

  @override
  String toString() => 'TranscriptRow($id, $depth)';
}

/// The rows a transcript draws, in order: the roots, each followed by its
/// children unless the user folded it away.
///
/// The tree is the agent's. This walks it; it never regroups it (spec
/// decision 2).
List<TranscriptRow> flattenRows(StackStore store, ThreadViewState view) {
  final rows = <TranscriptRow>[];
  void walk(String id, int depth) {
    final node = store.node(id);
    if (node == null) return;
    if (node.kind != 'band' && node.kind != 'turn') {
      rows.add(TranscriptRow(id, depth));
    }
    if (view.folded(id)) return;
    for (final child in node.children ?? const <String>[]) {
      walk(child, depth + 1);
    }
  }

  for (final root in store.roots) {
    walk(root, 0);
  }
  return rows;
}

/// The place [nodeId] holds among its parent's children whose kind is `step`:
/// 1 for the first step, 2 for the second, whatever rows the tree puts between
/// them.
///
/// A step is numbered among steps, never among the rows drawn before it, so
/// the prompt above it does not make the first one read 2. The parent is found
/// by walking the same tree [flattenRows] walks: the store exposes the roots,
/// a node by id and its children, and no parent link, so the walk is what
/// keeps this true of the tree the rows were drawn from rather than of a
/// `parent` field that could disagree with it. A node it cannot place — one
/// the tree does not hold, or one that is not a step — is 0 rather than an
/// error: a row draws on whatever tree it is handed.
int stepOrdinal(StackStore store, String nodeId) {
  if (store.node(nodeId)?.kind != 'step') return 0;
  // A step at the top of the tree is placed among the roots.
  var siblings = store.roots;
  if (!siblings.contains(nodeId)) {
    siblings = const <String>[];
    final stack = <String>[...store.roots];
    final seen = <String>{};
    while (stack.isNotEmpty) {
      final id = stack.removeLast();
      if (!seen.add(id)) continue;
      final children = store.node(id)?.children ?? const <String>[];
      if (children.contains(nodeId)) {
        siblings = children;
        break;
      }
      stack.addAll(children);
    }
  }
  var ordinal = 0;
  for (final id in siblings) {
    if (store.node(id)?.kind == 'step') ordinal++;
    if (id == nodeId) break;
  }
  return ordinal;
}

/// The transcript (design-system.md §5): a lazily built list over the store's
/// tree, drawing each node with the row for its kind at the view's density.
///
/// It follows the live end while the user is at the end. Scrolling up stops
/// following and shows one "Jump to live" control carrying the live headline;
/// returning to the end, or taking that control, resumes (decision 6).
class Transcript extends StatefulWidget {
  const Transcript({
    super.key,
    required this.store,
    required this.view,
    this.selection,
    this.asks = const [],
    this.onAnswer,
    this.now,
    this.onOpenNode,
    this.onOpenHandoff,
    this.onOpenAssignment,
    this.onRetryAssignment,
  });

  /// The session's tree.
  final StackStore store;

  /// The user's choices: density, folds, cursor, and whether it follows.
  final ThreadViewState view;

  /// The text selection across the transcript. Null gives this transcript its
  /// own, which it disposes with itself.
  final TranscriptSelection? selection;

  /// The asks waiting in this thread, pinned at the step that raised each
  /// (spec decision 9).
  final List<model.Ask> asks;

  /// Answers an ask where it is pinned.
  final AnswerAsk? onAnswer;

  /// What a waiting time is measured against.
  final DateTime? now;

  /// Opens a node's detail. Null leaves rows that are not controls plain.
  final void Function(String nodeId)? onOpenNode;

  /// Opens the coordinated task this thread handed work to. Null leaves the
  /// handoff's link out rather than drawing one that goes nowhere.
  final void Function(Place place)? onOpenHandoff;

  /// Opens one assignment's thread — the worker's, or the thread that was
  /// linked. Null leaves a row without a link.
  final void Function(Place place)? onOpenAssignment;

  /// Retries one failed assignment, by its identity, and nothing else. Null
  /// leaves the failure a fact rather than an action.
  final void Function(String assignmentId)? onRetryAssignment;

  @override
  State<Transcript> createState() => _TranscriptState();
}

class _TranscriptState extends State<Transcript> {
  final _controller = ScrollController();
  late final TranscriptSelection _selection =
      widget.selection ?? TranscriptSelection(textOf: widget.store.plainText);
  List<TranscriptRow> _rows = const [];

  @override
  void initState() {
    super.initState();
    widget.store.addListener(_onStore);
    widget.view.addListener(_onView);
    _controller.addListener(_onScroll);
    _rows = flattenRows(widget.store, widget.view);
    if (widget.view.following) _followSoon();
  }

  @override
  void didUpdateWidget(Transcript old) {
    super.didUpdateWidget(old);
    if (old.store != widget.store) {
      old.store.removeListener(_onStore);
      widget.store.addListener(_onStore);
    }
    if (old.view != widget.view) {
      old.view.removeListener(_onView);
      widget.view.addListener(_onView);
    }
  }

  @override
  void dispose() {
    widget.store.removeListener(_onStore);
    widget.view.removeListener(_onView);
    _controller.dispose();
    if (widget.selection == null) _selection.dispose();
    super.dispose();
  }

  void _onStore() {
    final rows = flattenRows(widget.store, widget.view);
    final grew = rows.length != _rows.length;
    _rows = rows;
    setState(() {});
    if (grew && widget.view.following) _followSoon();
  }

  void _onView() {
    _rows = flattenRows(widget.store, widget.view);
    setState(() {});
    if (widget.view.following) _followSoon();
  }

  /// Takes the view to the end after the frame that made it longer.
  void _followSoon() {
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted || !_controller.hasClients) return;
      _controller.jumpTo(_controller.position.maxScrollExtent);
    });
  }

  /// The distance from the end within which the view still counts as at it.
  static const _endSlop = Sizes.touch;

  bool _atEnd(ScrollPosition position) =>
      !position.hasContentDimensions ||
      position.maxScrollExtent - position.pixels <= _endSlop;

  void _onScroll() {
    if (!_controller.hasClients) return;
    final atEnd = _atEnd(_controller.position);
    if (atEnd == widget.view.following) return;
    widget.view.following = atEnd;
  }

  /// The asks pinned after one row: those whose step is this row, and — after
  /// the last row — those whose request named no step, or a step this tree
  /// does not hold (a folded or pruned row cannot strand a pending ask).
  List<model.Ask> _asksFor(String rowId, bool last) {
    final now = widget.now ?? DateTime.now();
    final held = <String>{};
    for (final row in _rows) {
      final at = row.id.lastIndexOf('step:');
      if (at >= 0) held.add(row.id.substring(at + 'step:'.length));
    }
    return [
      for (final ask in widget.asks)
        if ((ask.step != null && rowId.endsWith('step:${ask.step}')) ||
            (last && (ask.step == null || !held.contains(ask.step))))
          ask,
    ].where((ask) => ask.since.isBefore(now)).toList();
  }

  /// Every ask, for a transcript with no row to pin one to: with nothing to
  /// attach a card to, all of them are shown, including one whose request
  /// names a step this tree does not hold. What is waiting on the person is
  /// never dropped because the transcript is still empty.
  List<model.Ask> get _allAsks {
    final now = widget.now ?? DateTime.now();
    return [
      for (final ask in widget.asks)
        if (ask.since.isBefore(now)) ask,
    ];
  }

  /// The live headline a "Jump to live" control carries: the newest row's
  /// first line, which is what the agent is doing now.
  String get _liveHeadline {
    for (final row in _rows.reversed) {
      final step = widget.store.node(row.id)?.step;
      if (step != null) return step.headline;
    }
    for (final row in _rows.reversed) {
      final text = widget.store.plainText(row.id);
      if (text.isEmpty) continue;
      return text.split('\n').first;
    }
    return 'Working';
  }

  void _jumpToLive() {
    widget.view.following = true;
    if (_controller.hasClients) {
      _controller.jumpTo(_controller.position.maxScrollExtent);
    }
  }

  @override
  Widget build(BuildContext context) {
    final following = widget.view.following;
    final handoff = widget.view.handoff;
    return Column(
      children: [
        if (handoff != null)
          _HandoffHeader(
            handoff: handoff,
            now: widget.now ?? DateTime.now(),
            onOpenTask: widget.onOpenHandoff,
            onOpenAssignment: widget.onOpenAssignment,
            onRetry: widget.onRetryAssignment,
          ),
        if (!following && _rows.isNotEmpty)
          Padding(
            padding: const EdgeInsets.symmetric(vertical: Space.s4),
            child: Btn(
              'Jump to live · $_liveHeadline',
              icon: Glyphs.arrow_down,
              onTap: _jumpToLive,
            ),
          ),
        Expanded(
          child: SelectionLayer(
            selection: _selection,
            store: widget.store,
            order: [for (final row in _rows) row.id],
            controller: _controller,
            child: _rows.isEmpty
                ? _asksAlone()
                : ListView.builder(
                    controller: _controller,
                    padding: const EdgeInsets.symmetric(
                      horizontal: Layout.pageSide,
                      vertical: Space.s12,
                    ),
                    itemCount: _rows.length,
                    itemBuilder: (context, index) {
                      final row = _rows[index];
                      final pinned = _asksFor(
                        row.id,
                        index == _rows.length - 1,
                      );
                      return Align(
                        alignment: Alignment.topCenter,
                        child: ConstrainedBox(
                          constraints: const BoxConstraints(
                            maxWidth: Layout.reading,
                          ),
                          child: Column(
                            crossAxisAlignment: CrossAxisAlignment.stretch,
                            children: [
                              SelectableRow(
                                nodeId: row.id,
                                child: _Row(
                                  nodeId: row.id,
                                  depth: row.depth,
                                  store: widget.store,
                                  view: widget.view,
                                  onOpen: widget.onOpenNode,
                                ),
                              ),
                              for (final ask in pinned)
                                _PinnedAsk(
                                  ask: ask,
                                  now: widget.now,
                                  onAnswer: widget.onAnswer,
                                ),
                            ],
                          ),
                        ),
                      );
                    },
                  ),
          ),
        ),
      ],
    );
  }

  /// The asks alone, when the transcript has no row to pin them to: the same
  /// [_PinnedAsk] the rows carry, so a thread that has not happened yet still
  /// offers what is waiting on the person.
  Widget _asksAlone() {
    final asks = _allAsks;
    return ListView(
      controller: _controller,
      padding: const EdgeInsets.symmetric(
        horizontal: Layout.pageSide,
        vertical: Space.s12,
      ),
      children: [
        for (final ask in asks)
          Align(
            alignment: Alignment.topCenter,
            child: ConstrainedBox(
              constraints: const BoxConstraints(maxWidth: Layout.reading),
              child: _PinnedAsk(
                ask: ask,
                now: widget.now,
                onAnswer: widget.onAnswer,
              ),
            ),
          ),
      ],
    );
  }
}

/// The handoff a thread is the source of, capped and scrolled above the
/// conversation: it belongs to the thread rather than to one row, so it stays
/// where a person can see it while the work it sent runs elsewhere.
class _HandoffHeader extends StatelessWidget {
  const _HandoffHeader({
    required this.handoff,
    required this.now,
    this.onOpenTask,
    this.onOpenAssignment,
    this.onRetry,
  });

  final HandoffView handoff;
  final DateTime now;
  final void Function(Place place)? onOpenTask;
  final void Function(Place place)? onOpenAssignment;
  final void Function(String assignmentId)? onRetry;

  @override
  Widget build(BuildContext context) {
    return ConstrainedBox(
      constraints: const BoxConstraints(maxHeight: Layout.inspector),
      child: SingleChildScrollView(
        padding: const EdgeInsets.fromLTRB(
          Layout.pageSide,
          Space.s12,
          Layout.pageSide,
          0,
        ),
        child: Align(
          alignment: Alignment.topCenter,
          heightFactor: 1,
          child: ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: Layout.reading),
            child: HandoffBand(
              handoff: handoff,
              now: now,
              onOpenTask: onOpenTask,
              onOpenAssignment: onOpenAssignment,
              onRetry: onRetry,
            ),
          ),
        ),
      ),
    );
  }
}

/// One thread's handoff (agent-first Studio, task 7): the coordinated task the
/// thread handed work to, a row per target project with its state and thread
/// link, and the result the daemon reported back.
///
/// It draws what the model holds and posts nothing itself: opening the task, a
/// thread and retrying one assignment are the host's actions, so the same band
/// serves a thread's transcript and Marshal's own destination. A blocked
/// assignment shows the ask it is waiting on; a failed one offers a retry that
/// names exactly that assignment; a view whose read failed says how old it is
/// rather than presenting it as current.
class HandoffBand extends StatelessWidget {
  const HandoffBand({
    super.key,
    required this.handoff,
    required this.now,
    this.onOpenTask,
    this.onOpenAssignment,
    this.onRetry,
  });

  /// The handoff to draw.
  final HandoffView handoff;

  /// What a row's age is measured against.
  final DateTime now;

  /// Opens the coordinated task; null leaves the header a fact.
  final void Function(Place place)? onOpenTask;

  /// Opens an assignment's thread; null leaves the row without a link.
  final void Function(Place place)? onOpenAssignment;

  /// Retries one failed assignment; null leaves the failure a fact.
  final void Function(String assignmentId)? onRetry;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final task = handoff.task;
    final result = (handoff.result ?? '').trim();
    return Box(
      // Something waiting on a person makes the group draw attention; a failure
      // does not shout, because its row already says so in words and colour.
      tone: handoff.blocked ? BoxTone.attention : BoxTone.plain,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Label(
            'Handoff',
            count: handoff.rows.length,
            link: task == null || onOpenTask == null
                ? null
                : LabelLink(
                    'Open Marshal task',
                    onTap: () => onOpenTask!(task),
                  ),
          ),
          const SizedBox(height: Space.s8),
          if (task == null)
            Text(
              'Marshal is coordinating this thread’s work.',
              style: e.small.tint(e.muted),
            )
          else
            Text(
              'Marshal is coordinating this thread’s work in “${handoff.name}”.',
              style: e.small.tint(e.muted),
            ),
          const SizedBox(height: Space.s8),
          if (handoff.rows.isEmpty)
            Text(
              'Nothing has been assigned to another project yet.',
              style: e.small.tint(e.muted),
            )
          else
            Rows([
              for (final row in handoff.rows)
                RowItem(
                  name: row.name,
                  description: _description(row),
                  value: row.updatedAt == null
                      ? null
                      : relativeAge(row.updatedAt!, now),
                  onTap: row.thread == null || onOpenAssignment == null
                      ? null
                      : () => onOpenAssignment!(row.thread!),
                ),
            ]),
          for (final row in handoff.rows)
            if (row.retryable && onRetry != null)
              Padding(
                padding: const EdgeInsets.only(top: Space.s8),
                child: Align(
                  alignment: Alignment.centerLeft,
                  child: Btn(
                    'Retry ${row.name}',
                    icon: Glyphs.arrow_clockwise,
                    onTap: () => onRetry!(row.id),
                  ),
                ),
              ),
          if (result.isNotEmpty) ...[
            const SizedBox(height: Space.s12),
            const Label('Result'),
            const SizedBox(height: Space.s4),
            Notice(text: result),
            if (handoff.resultAt != null)
              Text(
                'Reported ${relativeAge(handoff.resultAt!, now)} ago',
                style: e.monoSmall.tint(e.muted),
              ),
          ],
          if (handoff.stale) ...[
            const SizedBox(height: Space.s8),
            Text(
              handoff.observedAt == null
                  ? 'This handoff could not be read again, so it may be behind.'
                  : 'Last observed ${relativeAge(handoff.observedAt!, now)} ago; '
                        'this handoff may be behind.',
              style: e.small.tint(e.muted),
            ),
          ],
        ],
      ),
    );
  }

  /// What one assignment's row says: where it stands, the thread it resolved to,
  /// and — for one blocked on a person — the ask it is waiting on, in the ask's
  /// own words.
  static String _description(HandoffAssignmentRow row) {
    final parts = <String>[row.stateWords];
    if (row.threadTitle.trim().isNotEmpty) {
      parts.add('thread: ${row.threadTitle.trim()}');
    }
    if (row.blocked && row.askLine.isNotEmpty) {
      parts.add('ask: ${row.askLine}');
    }
    return parts.join(' · ');
  }
}

/// One ask, pinned in the transcript at the step that raised it: the same
/// `AskCard` the Inbox draws, so answering it here or there is one action
/// (spec decision 9).
class _PinnedAsk extends StatelessWidget {
  const _PinnedAsk({required this.ask, this.now, this.onAnswer});

  final model.Ask ask;
  final DateTime? now;
  final AnswerAsk? onAnswer;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.only(left: Sizes.row, bottom: Space.s12),
      child: AnswerableAsk(
        // The card's own state — a reply field, what was typed, the last
        // action — belongs to this ask, not to this position: an ask answered
        // and dropped must not hand its open field to the one that takes its
        // place (F9 decision 9, the same key the Inbox and the Overview use).
        key: ValueKey('${ask.host}|${ask.id}'),
        ask: ask,
        placement: AskPlacement.thread,
        now: now ?? DateTime.now(),
        onAnswer: onAnswer,
      ),
    );
  }
}

/// One row, drawn by the node's kind at the view's density.
class _Row extends StatelessWidget {
  const _Row({
    required this.nodeId,
    required this.depth,
    required this.store,
    required this.view,
    this.onOpen,
  });

  final String nodeId;
  final int depth;
  final StackStore store;
  final ThreadViewState view;
  final void Function(String nodeId)? onOpen;

  @override
  Widget build(BuildContext context) {
    final node = store.node(nodeId);
    if (node == null) return const SizedBox.shrink();
    final density = view.density;
    final compact = density == Density.outline;
    final full = density == Density.full;
    final selected = view.cursor == nodeId;
    final pad = Padding(
      padding: const EdgeInsets.only(bottom: Space.s4),
      child: _body(
        context,
        node,
        compact: compact,
        full: full,
        selected: selected,
      ),
    );
    if (depth == 0) return pad;
    return Padding(
      padding: const EdgeInsets.only(left: Space.s24),
      child: pad,
    );
  }

  Widget _body(
    BuildContext context,
    WireNode node, {
    required bool compact,
    required bool full,
    required bool selected,
  }) {
    switch (node.kind) {
      case 'message':
      case 'final':
        final message = node.message;
        if (message == null || compact) return const SizedBox.shrink();
        if (message.role == 'user') return TurnPrompt(text: message.content);
        // A system message is the harness speaking: a snapshot warning, a
        // rollback, a compaction. Drawn as narration it would read as the
        // agent's own words (design-system.md §5).
        if (message.role == 'system') return Notice(text: message.content);
        return Narration(text: message.content);
      case 'step':
        final step = node.step;
        if (step == null) return const SizedBox.shrink();
        final foldable = (node.children ?? const <String>[]).isNotEmpty;
        return StepHeader(
          number: '${stepOrdinal(store, nodeId)}',
          headline: step.headline,
          took: _took(step),
          live: node.live ?? false,
          foldable: foldable,
          folded: view.folded(nodeId),
          onTap: foldable
              ? () => view.toggleFold(nodeId)
              : (onOpen == null ? null : () => onOpen!(nodeId)),
        );
      case 'tool':
        final tool = node.tool;
        if (tool == null) return const SizedBox.shrink();
        return Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            for (final call in tool.calls ?? const <WireCall>[])
              ToolRow(
                verb: tool.verb ?? tool.display,
                target: call.target ?? call.summaryLine ?? call.summary ?? '',
                result: compact ? null : _result(call),
                outcome: _outcome(call),
                selected: selected,
                onTap: onOpen == null ? null : () => onOpen!(nodeId),
              ),
            for (final call
                in full
                    ? (tool.calls ?? const <WireCall>[])
                    : const <WireCall>[])
              if ((call.output ?? '').isNotEmpty)
                Padding(
                  padding: const EdgeInsets.only(
                    left: Sizes.row,
                    bottom: Space.s6,
                  ),
                  child: OutputBlock(
                    text: call.output!,
                    truncated: call.truncated ?? false,
                  ),
                ),
            if (tool.running != null)
              LiveRow(
                headline:
                    '${tool.verb ?? tool.display} ${tool.running!.subject ?? tool.running!.target ?? ''}'
                        .trim(),
                detail: tool.running!.output,
                mono: true,
              ),
          ],
        );
      case 'completion':
        final completion = node.completion;
        if (completion == null || compact) return const SizedBox.shrink();
        // A finished job is a notice: its label and outcome lead, its summary
        // keeps its place beside them, and the error it carries, when it
        // carries one, is the second line, folded — otherwise a failure reads
        // exactly like a finish (design-system.md §5; spec: reports and
        // errors are distinct).
        final head = [
          '${completion.label} ${completion.outcome}',
          if (completion.summary != null) completion.summary!,
        ].join(' · ');
        final text = [
          head,
          if ((completion.error ?? '').isNotEmpty) completion.error!,
        ].join('\n');
        return Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Notice(text: text),
            if (full && (completion.output ?? '').isNotEmpty)
              OutputBlock(
                text: completion.output!,
                truncated: completion.truncated ?? false,
              ),
          ],
        );
      case 'thinking':
        final thinking = node.thinking;
        if (thinking == null || compact) return const SizedBox.shrink();
        // Thinking names itself on its first line and starts folded, so it
        // never reads as the agent's reply (design-system.md §5).
        return Notice(text: 'Thinking\n${thinking.text}');
      case 'runEvent':
        final event = node.runEvent;
        if (event == null) return const SizedBox.shrink();
        // The kind, led by a capital, opens the notice, so a run event does
        // not read as the agent narrating its own steps. Title, body (full
        // density only) and detail keep their order beneath it.
        final kind = _capitalize(event.kind.trim());
        final head = [
          if (kind.isNotEmpty) kind,
          if (event.title != null) event.title!,
        ].join(' · ');
        final text = [
          if (head.isNotEmpty) head,
          if (full && event.body != null) event.body!,
          if (event.detail != null) event.detail!,
        ].join('\n');
        return Notice(text: text);
      case 'plan':
        final plan = node.plan;
        if (plan == null) return const SizedBox.shrink();
        // How far the plan has got leads, always: the active task is the
        // addition to the count, not a replacement for it (spec finding 3).
        final active = plan.activeText;
        return LiveRow(
          headline:
              '${plan.done} of ${plan.total}'
              '${active != null && active.isNotEmpty ? ' · $active' : ''}',
          failed: false,
        );
      case 'receipt':
        final receipt = node.receipt;
        if (receipt == null || compact) return const SizedBox.shrink();
        // The receipt closes the turn, so it stands apart from the rows above
        // it and from the ask pinned beneath it (spec finding 4).
        return Padding(
          padding: const EdgeInsets.only(top: Space.s8, bottom: Space.s12),
          child: Text(
            [
              '${receipt.steps} steps',
              '${receipt.tools} calls',
              '${receipt.files} files',
              if (receipt.usage != null) receipt.usage!,
            ].join(' · '),
            style: Ember.of(context).monoSmall.tint(Ember.of(context).muted),
          ),
        );
      default:
        final text = store.plainText(nodeId);
        if (text.isEmpty || compact) return const SizedBox.shrink();
        return Narration(text: text);
    }
  }

  /// The kind with its first letter in capitals: a notice's leading word.
  static String _capitalize(String text) =>
      text.isEmpty ? text : '${text[0].toUpperCase()}${text.substring(1)}';

  /// A step's duration, as the header prints it: seconds under a minute, then
  /// minutes and seconds.
  static String? _took(WireStep step) {
    final start = step.startedAt;
    final end = step.endedAt;
    if (start == null || end == null || end <= start) return null;
    final seconds = (end - start) ~/ 1000;
    if (seconds < 60) return '${seconds}s';
    return '${seconds ~/ 60}m ${(seconds % 60).toString().padLeft(2, '0')}s';
  }

  static String? _result(WireCall call) {
    if ((call.error ?? '').isNotEmpty) return call.error;
    if (call.exitCode != null) return 'exit ${call.exitCode}';
    if ((call.summaryLine ?? '').isNotEmpty) return call.summaryLine;
    if ((call.summary ?? '').isNotEmpty) return call.summary;
    if ((call.files ?? const <String>[]).isNotEmpty) {
      final count = call.files!.length;
      return '$count ${count == 1 ? 'file' : 'files'}';
    }
    return null;
  }

  static ToolOutcome _outcome(WireCall call) {
    if ((call.failed ?? false) || (call.error ?? '').isNotEmpty) {
      return ToolOutcome.failed;
    }
    return ToolOutcome.ok;
  }
}
