import '../../model/fleet_store.dart' show describeFailure;
import '../../model/watches_model.dart';
import '../kit/kit.dart';
import '../sections/section_page.dart';
import 'automations_place.dart' show AutomationsTabs;
import 'confirm_action.dart';
import 'watch_form.dart';

/// The Watches tab of Automations (F7 decision 4): what is watched, on what
/// condition, a day's readings, the action on trip and the state.
///
/// A watch is started here, by an agent through the same route, and stopped
/// from its detail; what the detail never does is edit one in place.
class WatchesTab extends StatelessWidget {
  const WatchesTab({super.key, required this.model, this.tabs, this.state});

  final WatchesModel model;

  /// The tab bar the section page draws; null when the tab is shown alone.
  final AutomationsTabs? tabs;

  /// The state the shell knows.
  final StateKind? state;

  @override
  Widget build(BuildContext context) {
    return SectionPage<WatchInfo>(
      title: 'Watches',
      subtitle: 'What an agent set to watch, and what happens when it trips',
      model: model,
      tabs: tabs?.items,
      tabIndex: tabs?.index ?? 0,
      onTab: tabs?.onChanged,
      trailing: tabs?.trailing,
      state: state,
      readOnly: true,
      actions: [Btn('New watch', onTap: () => showWatchForm(context, model))],
      emptyAction: StateAction(
        'New watch',
        onTap: () => showWatchForm(context, model),
      ),
      detailTitle: (watch) => watch.name,
      emptyMessage:
          'A watch an agent starts — a command, a job or a file — is listed '
          'here with what it does when it trips.',
      row: (watch, onTap) => RowItem(
        name: watch.name,
        description: _about(watch),
        spark: watch.samples.isEmpty
            ? null
            : RowSpark([
                for (final sample in watch.samples) sample.value,
              ], label: 'Today’s readings of ${watch.name}'),
        value: _state(watch),
        onTap: onTap,
      ),
      detail: (context, watch) => _WatchDetail(model: model, watch: watch),
    );
  }

  /// What it watches, its condition, and the agent that started it.
  static String _about(WatchInfo watch) {
    final who = watch.agentID == 'studio'
        ? 'The bridge'
        : 'Agent ${watch.agentID}';
    final condition = watch.condition ?? '';
    final what = watch.kind.isEmpty ? 'a watch' : watch.kind;
    return condition.isEmpty ? '$who · $what' : '$who · $what · $condition';
  }

  /// Where it stands, in a word and an age.
  static String _state(WatchInfo watch) {
    switch (watch.state) {
      case 'watching':
        return 'Watching';
      case 'fired':
        return 'Tripped';
      case 'stopped':
        return 'Stopped';
      case 'error':
        return 'Failed';
      default:
        return watch.state.isEmpty ? 'Unknown' : watch.state;
    }
  }
}

/// A watch's detail: what it watches, what it does on a trip, and what the
/// last trip did. Nothing here is edited, so the section is read only.
class _WatchDetail extends StatefulWidget {
  const _WatchDetail({required this.model, required this.watch});

  final WatchesModel model;
  final WatchInfo watch;

  @override
  State<_WatchDetail> createState() => _WatchDetailState();
}

class _WatchDetailState extends State<_WatchDetail> {
  String? _note;
  String? _error;

  /// The reroute this detail has undone, so the action is not offered twice
  /// while the list still holds the row it was opened from.
  bool _undone = false;

  /// Whether the stop confirmation is showing, and whether it has been
  /// carried out for the row this detail was opened from.
  bool _confirmingStop = false;
  bool _stopped = false;

  /// The reroute the watch's rule plans, when it still carries one. It is
  /// what the trip will do, not a record anything can undo.
  RerouteRule? get _reroute => widget.watch.onTrip?.reroute;

  /// POST /api/reroutes/{id}/undo for the record the last trip made: the
  /// bridge puts the record's id on the watch row while it still holds it
  /// (lastReroute).
  Future<void> _undo() async {
    final reroute = widget.watch.lastReroute;
    if (reroute == null || _undone) return;
    setState(() {
      _note = null;
      _error = null;
    });
    try {
      await widget.model.undoReroute(reroute.id);
    } catch (error) {
      if (!mounted) return;
      setState(() => _error = describeFailure(error));
      return;
    }
    if (!mounted) return;
    setState(() {
      _undone = true;
      _note =
          'Undone: the role the trip rebound is back on its earlier binding.';
    });
  }

  /// DELETE /api/watches/{owner}/{id}: stops the watch on the session that
  /// started it, so its engine lets go of it.
  Future<void> _stop() async {
    if (_stopped) return;
    setState(() {
      _note = null;
      _error = null;
    });
    try {
      await widget.model.stop(widget.watch);
    } catch (error) {
      if (!mounted) return;
      setState(() {
        _confirmingStop = false;
        _error = describeFailure(error);
      });
      return;
    }
    if (!mounted) return;
    setState(() {
      _stopped = true;
      _confirmingStop = false;
    });
  }

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final watch = widget.watch;
    final lastError = watch.lastError ?? '';
    final lastSample = watch.lastSample ?? '';
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        _Fact(label: 'State', value: WatchesTab._state(watch)),
        _Fact(
          label: 'Source',
          value:
              watch.owner ??
              (watch.agentID == 'studio'
                  ? 'The bridge’s own session'
                  : 'Agent ${watch.agentID}'),
        ),
        _Fact(
          label: 'Condition',
          value: (watch.condition ?? '').isEmpty
              ? 'None: every reading counts'
              : watch.condition!,
        ),
        _Fact(
          label: 'Checked',
          value: watch.intervalMs <= 0
              ? 'Once'
              : 'Every ${_duration(watch.intervalMs)}',
        ),
        const SizedBox(height: Space.s16),
        Text(
          watch.fireCount == 1
              ? 'It has tripped once.'
              : 'It has tripped '
                    '${watch.fireCount} times.',
          style: e.body,
        ),
        const SizedBox(height: Space.s8),
        Text(
          'When it trips it ${_action(watch).toLowerCase()}.',
          style: e.body,
        ),
        if (lastSample.isNotEmpty) ...[
          const SizedBox(height: Space.s12),
          _Fact(label: 'Last reading', value: lastSample),
        ],
        if (watch.lastFiredAt != null && watch.lastFiredAt! > 0) ...[
          const SizedBox(height: Space.s12),
          _Fact(
            label: 'Last trip',
            value: '${widget.model.ageLabel(watch.lastFiredAt)} ago',
          ),
        ],
        if (lastError.isNotEmpty) ...[
          const SizedBox(height: Space.s12),
          Text(lastError, style: e.small.tint(e.err)),
        ],
        if (widget.watch.lastReroute case final reroute?) ...[
          const SizedBox(height: Space.s20),
          Btn('Undo the reroute', onTap: _undone ? null : _undo),
          const SizedBox(height: Space.s4),
          Text(
            _undone
                ? 'Its record is spent: the bridge will not undo it again.'
                : _rerouteAbout(reroute),
            style: e.small.tint(e.muted),
          ),
        ] else if (_reroute != null) ...[
          const SizedBox(height: Space.s20),
          Btn('Undo the reroute', onTap: null),
          const SizedBox(height: Space.s4),
          Text(
            'There is nothing to undo: the bridge records a reroute only when '
            'a trip makes one.',
            style: e.small.tint(e.muted),
          ),
        ],
        if (_stopped) ...[
          const SizedBox(height: Space.s20),
          Text(
            'Stopped: its engine has let go of it.',
            style: e.small.tint(e.muted),
          ),
        ] else if (_confirmingStop) ...[
          const SizedBox(height: Space.s20),
          ConfirmAction(
            question: 'Stop ${watch.name}?',
            loss:
                'Its readings end and it will not trip again. A watch an agent '
                'started is stopped for that agent too.',
            confirmLabel: 'Stop it',
            onConfirm: _stop,
            onCancel: () => setState(() => _confirmingStop = false),
          ),
        ] else ...[
          const SizedBox(height: Space.s20),
          Btn(
            'Stop this watch',
            onTap: () {
              setState(() {
                _note = null;
                _error = null;
                _confirmingStop = true;
              });
            },
          ),
        ],
        if (_note != null) ...[
          const SizedBox(height: Space.s12),
          Text(_note!, style: e.small.tint(e.muted)),
        ],
        if (_error != null) ...[
          const SizedBox(height: Space.s12),
          Text(_error!, style: e.small.tint(e.err)),
        ],
      ],
    );
  }

  static String _action(WatchInfo watch) => WatchesModel.describeOnTrip(watch);

  /// What the undo does, and when the trip that made the record happened.
  String _rerouteAbout(LastReroute reroute) {
    final age = widget.model.ageLabel(reroute.at);
    return age == 'now'
        ? 'Puts back the binding the trip changed just now.'
        : 'Puts back the binding the trip changed $age ago.';
  }

  /// A duration in the words §8 asks for: "5s", "2m", "1h".
  static String _duration(int millis) {
    final seconds = (millis / 1000).round();
    if (seconds < 60) return '${seconds}s';
    final minutes = (seconds / 60).round();
    if (minutes < 60) return '${minutes}m';
    return '${(minutes / 60).round()}h';
  }
}

/// One labelled fact of a read-only detail: a label and its value.
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
