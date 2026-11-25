import '../../model/age.dart';
import '../../model/marshal_model.dart';
import '../../model/place.dart';
import '../../model/thread_view_state.dart';
import '../kit/kit.dart';
import '../thread/transcript.dart' show HandoffBand;

/// Marshal's destination (agent-first Studio, task 5): the host-local
/// conversation a person resumes, the drafts New opens, and the read-only
/// attention result Marshal answers from.
///
/// It draws the model it is given and posts nothing itself (design-system.md
/// §5): the transcript and the composer are the thread's, built by the shell
/// from the same models a thread place reads, and every link is a [Place] the
/// model projected from a destination the daemon named. Nothing here decides
/// what is reachable — the host owns that.
class MarshalPlace extends StatelessWidget {
  const MarshalPlace({
    super.key,
    required this.model,
    required this.host,
    this.state,
    this.now,
    this.transcript,
    this.composer,
    this.onOpen,
    this.onRetry,
    this.onStop,
    this.stopping = false,
  });

  /// The host-local tasks, drafts and attention result.
  final MarshalModel model;

  /// The host whose daemon serves this destination, as the fleet names it.
  final String host;

  /// The destination's data state, when it is not ready (§7). [StateKind.stale]
  /// keeps the last attention result and marks it; the states that replace the
  /// page draw only their message.
  final StateKind? state;

  /// What a relative age is measured against.
  final DateTime? now;

  /// The open task's transcript, built by the shell from the session's own
  /// store — the thread's transcript, not a second one. Null when no task is
  /// open or the shell holds no bridge for it.
  final Widget? transcript;

  /// The open task's composer, built by the shell: the thread's composer,
  /// driving the coordinator session. Null when no task is open.
  final Widget? composer;

  /// Opens a place: an attention entry's source, a recent task, the task a
  /// draft became.
  final void Function(Place place)? onOpen;

  /// Reads the host again, for the one action a state offers.
  final VoidCallback? onRetry;

  /// Stops the open task at the requested scope (agent-first Studio, task 8).
  ///
  /// It is deliberately not the composer's Stop: that ends one turn in the
  /// coordinator conversation, while this stops the coordinated task — no further
  /// dispatch, its owned submissions withdrawn and the workers it created
  /// cancelled — and leaves a linked thread its own Stop. Null leaves the control
  /// out, for a host this build wired no route for.
  final VoidCallback? onStop;

  /// Whether the stop above is in flight: the control shows the stop as pending
  /// until the daemon confirms it.
  final bool stopping;

  DateTime get _now => now ?? DateTime.now();

  @override
  Widget build(BuildContext context) {
    final state = this.state;
    if (state != null && state != StateKind.stale) {
      return PageBody(
        title: 'Marshal',
        subtitle: 'What needs you across $host’s projects.',
        children: [_stateMessage(state)],
      );
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        _Bar(
          model: model,
          host: host,
          now: _now,
          onOpen: onOpen,
          onRetry: onRetry,
        ),
        const Hairline(),
        // The task's own scope control, apart from the composer's turn Stop: it
        // stops further dispatch and the workers this task created, and shows
        // that as pending until the daemon answers.
        if (model.task != null && (onStop != null || stopping))
          _TaskActions(model: model, stopping: stopping, onStop: onStop),
        Expanded(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              if (state == StateKind.stale)
                Padding(
                  padding: const EdgeInsets.fromLTRB(
                    Layout.pageSide,
                    Space.s12,
                    Layout.pageSide,
                    0,
                  ),
                  child: StateMessage.stale(
                    age: _cachedAge(),
                    detail: 'This is what Marshal last read.',
                  ),
                ),
              if (model.read != null || model.limited)
                Flexible(
                  flex: 2,
                  child: _ReadBand(
                    model: model,
                    host: host,
                    now: _now,
                    // The stale line above already says the answer could not be
                    // refreshed, so the band does not repeat it.
                    sayAge: state != StateKind.stale,
                    onOpen: onOpen,
                  ),
                ),
              // The work this task dispatched, and the result it reported back
              // into the thread it came from: the same band a source thread
              // draws, so the two surfaces cannot describe it differently.
              if (model.task != null && model.handoff != null)
                ConstrainedBox(
                  constraints: const BoxConstraints(
                    maxHeight: Layout.inspector,
                  ),
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
                        constraints: const BoxConstraints(
                          maxWidth: Layout.reading,
                        ),
                        child: HandoffBand(
                          handoff: model.handoff!,
                          now: _now,
                          onOpenTask: onOpen,
                          onOpenAssignment: onOpen,
                          onRetry: (id) => model.retryAssignment(id),
                        ),
                      ),
                    ),
                  ),
                ),
              Expanded(child: _body(context)),
            ],
          ),
        ),
        const Hairline(),
        _composerArea(context),
      ],
    );
  }

  /// The conversation: the open task's transcript, a draft's first prompt, or
  /// the recent tasks the entry offers.
  Widget _body(BuildContext context) {
    final task = model.task;
    if (task != null) {
      final transcript = this.transcript;
      if (transcript != null) return transcript;
      // Scrollable rather than a bare Column: the task's actions, the read and
      // the band above are fixed height, so a short window leaves this little
      // room and its message must not overflow it.
      return SingleChildScrollView(
        child: _Empty(
          message:
              'The conversation opens once this host’s bridge is wired; the '
              'read above is what Marshal found.',
          action: onRetry,
        ),
      );
    }
    if (model.draftId != null) {
      return PageBody(
        title: 'New Marshal task',
        subtitle:
            'Send the first message to start it on $host. Nothing is created '
            'until then.',
        children: const [],
      );
    }
    return _recent(context);
  }

  /// The entry: the host's recent tasks, newest first, and what New does.
  Widget _recent(BuildContext context) {
    final e = Ember.of(context);
    final tasks = model.tasks;
    return ListView(
      padding: const EdgeInsets.fromLTRB(
        Layout.pageSide,
        Layout.pageTop,
        Layout.pageSide,
        Space.s16,
      ),
      children: [
        const Label('Recent tasks'),
        const SizedBox(height: Space.s8),
        if (tasks.isEmpty)
          Padding(
            padding: const EdgeInsets.symmetric(vertical: Space.s8),
            child: Text(
              'No Marshal task yet: the one you start appears here.',
              style: e.small.tint(e.muted),
            ),
          )
        else
          Rows([
            for (final task in tasks)
              RowItem(
                name: task.name,
                description: task.state == MarshalTaskState.stopped
                    ? 'Stopped'
                    : 'Open',
                value: relativeAge(task.updatedAt, _now),
                onTap: onOpen == null
                    ? null
                    : () => onOpen!(Place.marshal(host: host, id: task.id)),
              ),
          ]),
      ],
    );
  }

  /// The composer, with the recipient named above it: Marshal, on this host.
  Widget _composerArea(BuildContext context) {
    final draft = model.draftId;
    final task = model.task;
    final Widget? field = draft != null
        ? MarshalDraftComposer(
            model: model,
            host: host,
            draftId: draft,
            onStarted: _started,
          )
        : (task != null ? composer : null);
    if (field == null) return const SizedBox.shrink();
    return Padding(
      padding: const EdgeInsets.fromLTRB(
        Layout.pageSide,
        Space.s12,
        Layout.pageSide,
        Space.s12,
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          _Recipient(host: host),
          const SizedBox(height: Space.s6),
          field,
        ],
      ),
    );
  }

  /// A draft's first send created its task: the address names the task now,
  /// so Back returns to the conversation rather than to a draft that is gone.
  void _started(MarshalTask task) {
    onOpen?.call(Place.marshal(host: host, id: task.id));
  }

  /// How old the answer on screen is, in the design's words.
  String _cachedAge() =>
      relativeAge(_now.subtract(model.cachedAge ?? Duration.zero), _now);

  Widget _stateMessage(StateKind state) {
    final retry = StateAction('Read again', onTap: onRetry ?? () {});
    return switch (state) {
      StateKind.loading => const StateMessage.loading(lines: 4),
      // The empty page replaces the bar, and with it the menu's New task, so
      // its one action is that New: a person told to start a task must be
      // given the control that starts one.
      StateKind.empty => StateMessage.empty(
        message:
            'No Marshal task on $host yet. Start one, and the asks this host '
            'holds show up here once it has.',
        action: StateAction(
          'New task',
          onTap: () => onOpen?.call(Place.marshalDraft(model.newDraft())),
        ),
      ),
      StateKind.error => StateMessage.error(
        message: model.failure ?? 'Marshal could not read this host.',
        retry: retry,
      ),
      StateKind.unavailable => StateMessage.unavailable(
        host: host,
        lastSeen: model.readAt == null
            ? null
            : relativeAge(model.readAt!, _now),
        action: retry,
      ),
      // A refused token is why nothing is here, and no retry clears one.
      StateKind.unauthorized => StateMessage.unauthorized(host: host),
      StateKind.stale => StateMessage.stale(age: _cachedAge()),
      StateKind.planned => StateMessage.planned(
        name: 'Marshal',
        note:
            'Marshal is built; it opens once the shell wires the host’s '
            'bridge',
      ),
    };
  }
}

/// The task's own scope control (agent-first Studio, task 8).
///
/// It is the coordinated task's Stop, not the conversation's: the composer above
/// ends one turn, while this stops further dispatch, withdraws the task's owned
/// submissions and cancels the workers it created — and says so as pending until
/// the daemon confirms it, so a person is never told a task stopped before it
/// did. A linked thread keeps its own Stop, which the sentence here says.
class _TaskActions extends StatelessWidget {
  const _TaskActions({
    required this.model,
    required this.stopping,
    this.onStop,
  });

  final MarshalModel model;

  /// Whether the stop is in flight: the control is pending and says so.
  final bool stopping;

  final VoidCallback? onStop;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final failure = model.stopFailure;
    return Padding(
      padding: const EdgeInsets.fromLTRB(
        Layout.pageSide,
        Space.s8,
        Layout.pageSide,
        0,
      ),
      child: Row(
        children: [
          Flexible(
            child: Tip(
              message:
                  'Stops this coordinated task: no further dispatch, its waiting '
                  'submissions withdrawn and the workers it started cancelled. A '
                  'linked thread keeps its own Stop.',
              child: Btn(
                stopping ? 'Stopping…' : 'Stop task',
                onTap: stopping ? null : onStop,
              ),
            ),
          ),
          if (failure != null) ...[
            const SizedBox(width: Space.s8),
            Flexible(
              child: Text(
                failure,
                maxLines: 2,
                overflow: TextOverflow.ellipsis,
                style: e.small.tint(e.err),
              ),
            ),
          ],
        ],
      ),
    );
  }
}

/// The recipient, named above the composer as the design doc asks: a person
/// always knows who they are talking to.
class _Recipient extends StatelessWidget {
  const _Recipient({required this.host});

  final String host;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Row(
      children: [
        Text('To Marshal', style: e.bodyStrong),
        const SizedBox(width: Space.s8),
        Flexible(
          child: Text(
            'on $host',
            maxLines: 1,
            overflow: TextOverflow.ellipsis,
            style: e.small.tint(e.muted),
          ),
        ),
      ],
    );
  }
}

/// The destination's own bar: what it is, the host it reads, the tasks a
/// person can switch to, and the read again action.
class _Bar extends StatelessWidget {
  const _Bar({
    required this.model,
    required this.host,
    required this.now,
    this.onOpen,
    this.onRetry,
  });

  final MarshalModel model;
  final String host;
  final DateTime now;
  final void Function(Place place)? onOpen;
  final VoidCallback? onRetry;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final menu = Menu(
      choices: [
        for (final task in model.tasks)
          MenuChoice(
            task.name,
            '${task.state == MarshalTaskState.stopped ? 'Stopped' : 'Open'} · '
            '${relativeAge(task.updatedAt, now)}',
            onSelect: () {
              model.selectTask(task.id);
              onOpen?.call(Place.marshal(host: host, id: task.id));
            },
          ),
        MenuChoice(
          'New task',
          'A fresh Marshal conversation on $host',
          onSelect: () => _newTask(context),
        ),
      ],
      anchor: (context, toggle) => Btn(
        model.task?.name ?? 'Recent tasks',
        icon: Glyphs.chevron_up_chevron_down,
        onTap: toggle,
      ),
    );
    return Padding(
      padding: const EdgeInsets.fromLTRB(
        Layout.pageSide,
        Space.s12,
        Layout.pageSide,
        Space.s12,
      ),
      child: Row(
        children: [
          Expanded(
            child: Row(
              children: [
                Flexible(
                  child: Text(
                    'Marshal',
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: e.heading,
                  ),
                ),
                const SizedBox(width: Space.s8),
                Flexible(child: Pill(host, tooltip: 'The host Marshal reads')),
              ],
            ),
          ),
          const SizedBox(width: Space.s8),
          Flexible(child: menu),
          if (onRetry != null) ...[
            const SizedBox(width: Space.s8),
            IconBtn(
              Glyphs.arrow_clockwise,
              tooltip: 'Read what needs you again',
              onTap: onRetry,
            ),
          ],
        ],
      ),
    );
  }

  /// New opens a draft with its own id: the address names it, so Back returns
  /// to the same draft, and its first send creates the task once.
  void _newTask(BuildContext context) {
    final draft = model.newDraft();
    onOpen?.call(Place.marshalDraft(draft));
  }
}

/// The read-only attention result: every entry with its kind, text, age and
/// where it opens, and what the read could not cover.
class _ReadBand extends StatelessWidget {
  const _ReadBand({
    required this.model,
    required this.host,
    required this.now,
    required this.sayAge,
    this.onOpen,
  });

  final MarshalModel model;

  /// The host the read came from, which the band names when it could not be
  /// reached again.
  final String host;

  final DateTime now;

  /// Whether to say how old a cached answer is here; false when the caller
  /// already drew that line above.
  final bool sayAge;

  final void Function(Place place)? onOpen;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final read = model.read;
    return ListView(
      padding: const EdgeInsets.fromLTRB(
        Layout.pageSide,
        Space.s12,
        Layout.pageSide,
        Space.s8,
      ),
      children: [
        Label('Needs you', count: read?.entries.length),
        const SizedBox(height: Space.s8),
        if (read == null)
          Text(
            model.failure ?? 'Reading what needs you from $host…',
            style: e.small.tint(model.failure == null ? e.muted : e.err),
          )
        else if (read.entries.isEmpty)
          Text(
            model.cleared
                ? 'Nothing needs you across this host’s projects.'
                : 'Nothing to show from what could be read: this answer covers '
                      'less than the task’s projects.',
            style: e.small.tint(e.muted),
          )
        else
          Rows([
            for (final entry in read.entries)
              RowItem(
                name: entry.text,
                description: '${entry.kindLabel} · ${_where(entry)}',
                value: relativeAge(now.subtract(entry.age), now),
                onTap: entry.place == null || onOpen == null
                    ? null
                    : () => onOpen!(entry.place!),
              ),
          ]),
        if (read != null && read.limits.isNotEmpty) ...[
          const SizedBox(height: Space.s12),
          const Label('What this read could not cover'),
          const SizedBox(height: Space.s4),
          for (final source in read.limits)
            Padding(
              padding: const EdgeInsets.only(bottom: Space.s4),
              child: Text(
                source.reason == null
                    ? source.words
                    : '${source.words}: ${source.reason}',
                style: e.small.tint(e.muted),
              ),
            ),
        ],
        if (model.cachedOnly && sayAge) ...[
          const SizedBox(height: Space.s8),
          Text(
            'Read ${_age()} ago; $host could not be reached to read it again, '
            'so this answer may be out of date.',
            style: e.small.tint(e.muted),
          ),
        ] else if (model.reading) ...[
          const SizedBox(height: Space.s8),
          Text('Reading $host again…', style: e.small.tint(e.muted)),
        ],
      ],
    );
  }

  String _age() {
    final age = model.cachedAge ?? Duration.zero;
    return relativeAge(now.subtract(age), now);
  }

  /// Where an entry opens, in the words the place is named by: the row says
  /// which surface the link takes a person to (§8: say what will happen).
  static String _where(MarshalEntry entry) => switch (entry.place?.kind) {
    PlaceKind.thread => 'opens the thread',
    PlaceKind.inbox => 'opens Needs you',
    PlaceKind.pulls => 'opens Pull requests',
    _ => 'no link',
  };
}

/// The composer of a Marshal draft: the kit's composer, bound to the draft's
/// own view state, so two drafts never share what was typed and switching away
/// keeps it.
class MarshalDraftComposer extends StatefulWidget {
  const MarshalDraftComposer({
    super.key,
    required this.model,
    required this.host,
    required this.draftId,
    this.onStarted,
  });

  final MarshalModel model;
  final String host;
  final String draftId;

  /// What the draft became: the first send created the task.
  final void Function(MarshalTask task)? onStarted;

  @override
  State<MarshalDraftComposer> createState() => _MarshalDraftComposerState();
}

class _MarshalDraftComposerState extends State<MarshalDraftComposer> {
  late final TextEditingController _text = TextEditingController(
    text: _view.draft,
  );

  ThreadViewState get _view =>
      widget.model.viewFor(MarshalModel.viewKeyForDraft(widget.draftId));

  @override
  void initState() {
    super.initState();
    _view.addListener(_syncFromView);
    _text.addListener(_syncToView);
  }

  @override
  void didUpdateWidget(MarshalDraftComposer old) {
    super.didUpdateWidget(old);
    if (old.draftId != widget.draftId) {
      _syncFromView();
    }
  }

  @override
  void dispose() {
    _view.removeListener(_syncFromView);
    _text
      ..removeListener(_syncToView)
      ..dispose();
    super.dispose();
  }

  void _syncFromView() {
    if (_text.text == _view.draft) return;
    _text.text = _view.draft;
  }

  void _syncToView() {
    if (_view.draft == _text.text) return;
    _view.draft = _text.text;
  }

  Future<void> _send() async {
    final task = await widget.model.start(widget.draftId, _text.text);
    if (task == null || !mounted) return;
    // The task exists now: the draft's text belongs to its first message, and
    // the address names the task from here on.
    _view.draft = '';
    widget.onStarted?.call(task);
  }

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: Listenable.merge([widget.model, _view]),
      builder: (context, _) {
        final e = Ember.of(context);
        final failure = widget.model.failure;
        return Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Composer(
              controller: _text,
              hint: 'Ask Marshal what needs you…',
              // No session exists until the first send, so there is no mode to
              // name and none is claimed.
              mode: null,
              model: null,
              onSend: widget.model.starting ? null : () => _send(),
            ),
            if (failure != null)
              Padding(
                padding: const EdgeInsets.only(top: Space.s4),
                child: Text(failure, style: e.small.tint(e.err)),
              ),
          ],
        );
      },
    );
  }
}

/// A quiet line and one action, where a conversation would be.
class _Empty extends StatelessWidget {
  const _Empty({required this.message, this.action});

  final String message;
  final VoidCallback? action;

  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.fromLTRB(
      Layout.pageSide,
      Layout.pageTop,
      Layout.pageSide,
      Space.s16,
    ),
    child: StateMessage.empty(
      message: message,
      action: StateAction('Read again', onTap: action ?? () {}),
    ),
  );
}
