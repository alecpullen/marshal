import '../../model/watches_model.dart';
import '../kit/kit.dart';
import '../sections/section_page.dart';
import 'automations_place.dart' show AutomationsTabs;

/// The Watches tab of Automations (F7 decision 4): what is watched, on what
/// condition, a day's readings, the action on trip and the state.
///
/// A watch is started by an agent or by the bridge, so its detail is read
/// only: it says what the watch watches, what its last trip did, and — where
/// the action was a reroute — whether that can be undone.
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
      state: state,
      readOnly: true,
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

  /// The reroute the last trip made, when the row says it made one. The
  /// bridge does not report the reroute record's id, so nothing here can
  /// undo it yet; the note says so rather than offering a button that cannot
  /// work.
  RerouteRule? get _reroute => widget.watch.onTrip?.reroute;

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
        if (_reroute case final reroute?) ...[
          const SizedBox(height: Space.s20),
          Btn('Undo the reroute', onTap: null),
          const SizedBox(height: Space.s4),
          Text(
            'The bridge does not report which reroute ${reroute.role} → '
            '${reroute.preset} belongs to, so it cannot be undone from here '
            'yet.',
            style: e.small.tint(e.muted),
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
