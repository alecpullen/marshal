import 'dart:async';

import '../../model/age.dart';
import '../../model/planning_model.dart';
import '../kit/kit.dart';

/// A planning thread (F6 decision 8): the conversation with the planner in the
/// centre, the plan the last snapshot froze beside it, and the requests the
/// planner is parked on pinned above both.
///
/// The conversation is the planner's own journal, one line per thing that
/// happened, so what is read here is what the bridge recorded — the person's
/// prompt is drawn as they said it, and nothing is shown as done work before
/// the journal says it (design-system.md §7).
///
/// The screen takes a [PlanningModel] and callbacks and never fetches (§5):
/// the model reads the journal, sends prompts and captures snapshots.
class PlanningScreen extends StatefulWidget {
  const PlanningScreen({
    super.key,
    required this.plan,
    this.title = 'Planning',
    this.subtitle,
    this.state,
    this.now,
    this.onRetry,
    this.onHandToFactory,
  });

  final PlanningModel plan;

  /// What the thread is called: the project's name, or "Planning".
  final String title;

  /// One line beneath the title: the planner's access mode and where it runs.
  ///
  /// Kept for callers, and no longer drawn: the context strip states the
  /// connection, the mode (confirmed or only requested) and the workspace from
  /// the record itself, so a caller-supplied sentence — which cannot tell a
  /// requested mode from one in force — would be a second, softer claim about
  /// the same three facts.
  final String? subtitle;

  /// The thread's data state, when it is not ready (§7).
  final StateKind? state;

  /// What a relative age is measured against.
  final DateTime? now;

  final VoidCallback? onRetry;

  /// Opens the handoff review (F6 decision 8). Null leaves the action out, so
  /// a host that cannot hand off never offers it.
  final VoidCallback? onHandToFactory;

  @override
  State<PlanningScreen> createState() => _PlanningScreenState();
}

class _PlanningScreenState extends State<PlanningScreen> {
  final _draft = TextEditingController();

  DateTime get _now => widget.now ?? DateTime.now();

  @override
  void initState() {
    super.initState();
    widget.plan.addListener(_changed);
    unawaited(widget.plan.refresh());
  }

  @override
  void dispose() {
    widget.plan.removeListener(_changed);
    _draft.dispose();
    super.dispose();
  }

  void _changed() {
    if (mounted) setState(() {});
  }

  /// Sends the draft and clears the field, so the person's words appear once —
  /// in the conversation, where the model put them.
  Future<void> _send() async {
    final text = _draft.text.trim();
    if (text.isEmpty || widget.plan.running) return;
    _draft.clear();
    await widget.plan.prompt(text);
  }

  @override
  Widget build(BuildContext context) {
    final state = widget.state;
    final model = widget.plan;
    if (state != null && state != StateKind.stale) {
      return PageBody(title: widget.title, children: [_stateMessage(state)]);
    }
    return PageBody(
      title: widget.title,
      width: PageWidth.full,
      actions: [
        if (widget.onHandToFactory != null)
          Btn.primary(
            'Hand to the factory',
            icon: Glyphs.arrow_right,
            onTap: widget.onHandToFactory,
          ),
      ],
      children: [
        if (state == StateKind.stale) ...[
          StateMessage.stale(
            age: relativeAge(_lastActive(model), _now),
            detail: 'The planner has not answered; this is what it last said.',
          ),
          const SizedBox(height: Space.s16),
        ],
        if (model.requests.isNotEmpty) ...[
          Label('Waiting on you', count: model.requests.length),
          const SizedBox(height: Space.s8),
          for (final request in model.requests)
            Padding(
              padding: const EdgeInsets.only(bottom: Space.s8),
              child: _requestCard(model, request),
            ),
          const SizedBox(height: Space.s8),
        ],
        _contextStrip(model),
        const SizedBox(height: Space.s8),
        _conversation(model),
        if (model.failure != null) ...[
          const SizedBox(height: Space.s8),
          Text(
            model.failure!,
            style: Ember.of(context).small.tint(Ember.of(context).err),
          ),
        ],
        const SizedBox(height: Space.s16),
        Label('The plan', count: model.latest == null ? null : 1),
        const SizedBox(height: Space.s8),
        _planPanel(model),
        const SizedBox(height: Space.s16),
        Composer(
          controller: _draft,
          hint: model.running
              ? 'Steer the planner…'
              : 'Describe the work to plan…',
          running: model.running,
          mode: model.modeWords,
          onSend: model.running ? null : _send,
          onStop: model.running ? () => unawaited(model.cancel()) : null,
        ),
      ],
    );
  }

  /// The session's own facts, in a strip directly above the conversation:
  /// whether it is connected, the mode and whether the bridge confirmed it,
  /// and the workspace it runs in.
  ///
  /// Every fact is words first — a chip's colour is never the only thing it
  /// says (design-system.md §2) — and the tooltip says what the fact means for
  /// the person rather than repeating the words beside it (§9). Nothing here
  /// is guessed: each chip is one field of the record, or says that the record
  /// has not been read.
  Widget _contextStrip(PlanningModel model) {
    final connection = model.connection;
    final waiting = model.waiting;
    return Wrap(
      spacing: Space.s8,
      runSpacing: Space.s6,
      children: [
        Pill(connection.label, tooltip: connection.note),
        Pill.icon(
          model.modeWords,
          icon: Glyphs.shield,
          tooltip: _modeNote(model),
        ),
        Pill(
          model.workspaceWords,
          mono: model.workspace != null,
          tooltip: _workspaceNote(model),
        ),
        if (waiting > 0)
          Pill(
            '$waiting ${waiting == 1 ? 'request' : 'requests'} waiting on you',
          ),
      ],
    );
  }

  /// What the mode chip means: the mode in force, or why the one asked for is
  /// not in force yet.
  ///
  /// The record is matched first, so a record that was read but names no mode
  /// is never described as a record nobody read.
  static String _modeNote(PlanningModel model) => switch ((
    model.view,
    model.confirmedMode,
    model.requestedMode,
  )) {
    (null, _, _) =>
      'No planner record has been read, so no mode is stated as in force.',
    (_, _?, _) =>
      'The bridge confirmed it: this is the mode the session runs '
          'under.',
    (_, null, _?) =>
      'Only asked for: the bridge has not confirmed it, so it is not in force.',
    _ => 'The planner’s record names no mode, so none is stated as in force.',
  };

  /// What the workspace chip means.
  static String _workspaceNote(PlanningModel model) =>
      switch ((model.workspace, model.view)) {
        (_?, _) => 'Where this planner’s edits land.',
        (null, null) =>
          'No planner record has been read, so the workspace is not known.',
        _ => 'The planner’s record names no workspace, so none is stated here.',
      };

  /// The conversation: one row per journaled thing that happened.
  Widget _conversation(PlanningModel model) {
    final e = Ember.of(context);
    final messages = model.messages;
    return Box.flush(
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          if (messages.isEmpty)
            Padding(
              padding: const EdgeInsets.all(Space.s16),
              child: Text(
                'Nothing has been planned yet. Describe the work and the '
                'planner will draft it.',
                style: e.body.tint(e.muted),
              ),
            ),
          for (final (index, message) in messages.indexed) ...[
            if (index > 0) const Hairline(),
            Padding(
              padding: const EdgeInsets.symmetric(
                horizontal: Space.s16,
                vertical: Space.s12,
              ),
              child: message.fromUser
                  ? TurnPrompt(text: message.text)
                  : Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(message.label, style: e.bodyStrong),
                        if (message.text.isNotEmpty) ...[
                          const SizedBox(height: Space.s4),
                          Text(message.text, style: e.reading),
                        ],
                      ],
                    ),
            ),
          ],
        ],
      ),
    );
  }

  /// The plan the last snapshot froze, or the line that says there is none.
  Widget _planPanel(PlanningModel model) {
    final e = Ember.of(context);
    final latest = model.latest;
    if (latest == null) {
      return Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text(
            'No snapshot yet. Handing off freezes the plan and the spec.',
            style: e.body.tint(e.muted),
          ),
          if (widget.onHandToFactory != null) ...[
            const SizedBox(height: Space.s6),
            Text(_handoffNote(latest), style: e.small.tint(e.muted)),
          ],
        ],
      );
    }
    final plan = latest.plan.trim();
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Text(latest.summary, style: e.bodyStrong),
        const SizedBox(height: Space.s6),
        if (plan.isEmpty)
          Text('The snapshot froze no plan text.', style: e.body.tint(e.muted))
        else
          OutputBlock(text: plan),
        if (latest.spec.trim().isNotEmpty) ...[
          const SizedBox(height: Space.s8),
          Text(latest.spec.trim(), style: e.small.tint(e.muted)),
        ],
        const SizedBox(height: Space.s6),
        Text(
          'Snapshot ${_short(latest.sha256)} · '
          '${latest.files.length} '
          '${latest.files.length == 1 ? 'file' : 'files'}',
          style: e.monoSmall.tint(e.muted),
        ),
        if (latest.rejected.isNotEmpty) ...[
          const SizedBox(height: Space.s6),
          Label('Not captured', count: latest.rejected.length),
          const SizedBox(height: Space.s4),
          for (final rejected in latest.rejected)
            Padding(
              padding: const EdgeInsets.only(bottom: Space.s2),
              child: Text(
                '${rejected.path} — ${rejected.reason}',
                style: e.small.tint(e.muted),
              ),
            ),
        ],
        if (widget.onHandToFactory != null) ...[
          const SizedBox(height: Space.s6),
          Text(_handoffNote(latest), style: e.small.tint(e.muted)),
        ],
      ],
    );
  }

  /// What the handoff action does, in plain words, and where it leaves this
  /// conversation: the review opens over this screen and closing the review
  /// returns to it, with everything the screen holds still here.
  ///
  /// The action itself belongs to the place that owns the sheet, and its
  /// wording belongs to the review; this is only the sentence that says what
  /// it is for, so the person does not have to guess where it leads.
  String _handoffNote(PlannerSnapshot? latest) {
    // With no snapshot the line above already says what handing off freezes;
    // this adds only where it leaves the person.
    final opened = latest == null
        ? ''
        : 'Handing off opens the factory review with the capture digest '
              '${_short(latest.sha256)}. ';
    return '$opened'
        'Closing the review brings you back here: this conversation, its '
        'requests and your draft stay.';
  }

  /// One request as the kit's ask card: a permission takes allow or deny, a
  /// question takes the person's words.
  Widget _requestCard(PlanningModel model, PlannerRequest request) => AskCard(
    kind: request.permission ? AskKind.approval : AskKind.question,
    title: request.title,
    body: request.detail,
    waiting: relativeAge(_askedAt(), _now),
    placement: AskPlacement.thread,
    actions: request.permission
        ? [
            AskAction(
              'Allow',
              onTap: () => unawaited(model.answer(request.requestID, 'allow')),
            ),
            AskAction(
              'Deny',
              onTap: () => unawaited(model.answer(request.requestID, 'deny')),
            ),
          ]
        : [
            AskAction(
              'Answer',
              onTap: () => unawaited(_answerQuestion(model, request)),
            ),
          ],
  );

  /// Asks the person a question's answer, through the kit's sheet.
  Future<void> _answerQuestion(
    PlanningModel model,
    PlannerRequest request,
  ) async {
    final e = Ember.of(context);
    final controller = TextEditingController();
    final answer = await Sheet.show<String>(
      context,
      Sheet(
        title: 'Answer the planner',
        actions: [
          Btn.primary(
            'Answer',
            onTap: () => Navigator.maybePop(context, controller.text.trim()),
          ),
        ],
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Field.multiline(
              label: request.detail,
              controller: controller,
              hint: 'What the planner should do',
            ),
            const SizedBox(height: Space.s8),
            Text(
              'The planner continues with this answer and nothing else.',
              style: e.small.tint(e.muted),
            ),
          ],
        ),
      ),
    );
    controller.dispose();
    final text = answer?.trim() ?? '';
    if (text.isEmpty) return;
    await model.answer(request.requestID, text);
  }

  /// When a request was asked, as far as this build can tell: the newest
  /// journal line is the moment it was raised.
  DateTime _askedAt() {
    final messages = widget.plan.messages;
    for (final message in messages.reversed) {
      final at = message.at;
      if (at != null) return at;
    }
    return _now;
  }

  /// When the planner last said anything, for a stale banner.
  DateTime _lastActive(PlanningModel model) {
    for (final message in model.messages.reversed) {
      final at = message.at;
      if (at != null) return at;
    }
    return _now;
  }

  /// A digest short enough for a line: the first eight characters.
  static String _short(String hash) =>
      hash.length <= 8 ? hash : hash.substring(0, 8);

  Widget _stateMessage(StateKind state) {
    final retry = StateAction(
      'Read the planner again',
      onTap: widget.onRetry ?? () {},
    );
    return switch (state) {
      StateKind.loading => const StateMessage.loading(lines: 4),
      StateKind.empty => StateMessage.empty(
        message:
            'No planner here yet: describe the work and this thread drafts '
            'its plan.',
        action: retry,
      ),
      StateKind.error => StateMessage.error(
        message: 'The plan could not be read.',
        retry: retry,
      ),
      StateKind.unavailable => StateMessage.unavailable(
        host: widget.title,
        lastSeen: relativeAge(_now, _now),
        action: retry,
      ),
      StateKind.stale => const StateMessage.stale(age: 'now'),
      StateKind.planned => StateMessage.planned(
        name: 'Planning',
        note: 'Not built yet',
      ),
    };
  }
}
