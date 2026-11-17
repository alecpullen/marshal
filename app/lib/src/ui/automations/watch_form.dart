import '../../model/fleet_store.dart' show describeFailure;
import '../../model/watches_model.dart';
import '../kit/kit.dart';

/// The New watch form (F7 decision 4): what to watch, how often, and what to
/// do when it trips.
///
/// A watch an agent starts is started through the same route, so this form is
/// open to an agent id as well as the bridge's own control session. A reroute
/// is offered only for a Studio watch: it rewrites user-global config, which
/// no single agent's watch should be able to do.
Future<void> showWatchForm(BuildContext context, WatchesModel model) async {
  WatchChoices choices;
  try {
    choices = await model.choices();
  } catch (_) {
    // The reroute is optional, so a bridge that cannot list roles and presets
    // leaves it out rather than failing the whole form.
    choices = const WatchChoices();
  }
  if (!context.mounted) return;
  await Sheet.show<void>(
    context,
    Sheet(
      title: 'New watch',
      actions: [Btn('Cancel', onTap: () => Navigator.of(context).maybePop())],
      child: _WatchForm(model: model, choices: choices),
    ),
  );
}

/// The minimum interval the engine accepts, in seconds.
const int _minInterval = 2;

class _WatchForm extends StatefulWidget {
  const _WatchForm({required this.model, required this.choices});

  final WatchesModel model;
  final WatchChoices choices;

  @override
  State<_WatchForm> createState() => _WatchFormState();
}

class _WatchFormState extends State<_WatchForm> {
  final _owner = TextEditingController();
  final _name = TextEditingController();
  final _detail = TextEditingController();
  final _condition = TextEditingController();
  final _interval = TextEditingController(text: '30');

  String _kind = 'command';
  String _mode = 'once';
  bool _notify = true;
  bool _resume = false;
  bool _reroute = false;
  String _role = '';
  String _preset = '';
  String? _error;
  bool _starting = false;

  @override
  void dispose() {
    for (final controller in [_owner, _name, _detail, _condition, _interval]) {
      controller.dispose();
    }
    super.dispose();
  }

  /// The agent whose session starts it, or the bridge's own when empty.
  String get _agent => _owner.text.trim();

  bool get _isStudio => _agent.isEmpty || _agent == 'studio';

  String get _nameValue => _name.text.trim();

  String get _detailValue => _detail.text.trim();

  int get _seconds => int.tryParse(_interval.text.trim()) ?? 0;

  bool get _valid =>
      _nameValue.isNotEmpty &&
      _detailValue.isNotEmpty &&
      _seconds >= _minInterval &&
      (!_reroute || (!_isStudio) || (_role.isNotEmpty && _preset.isNotEmpty));

  WatchCreate get _request => WatchCreate(
    agentID: _isStudio ? '' : _agent,
    spec: WatchSpec(
      name: _nameValue,
      kind: _kind,
      command: _kind == 'command' ? _detailValue : '',
      jobID: _kind == 'job' ? _detailValue : '',
      path: _kind == 'file' ? _detailValue : '',
      condition: _condition.text.trim(),
      mode: _mode,
      intervalMs: _seconds * 1000,
      notify: _notify,
      resume: _resume,
    ),
    onTrip: _isStudio && _reroute
        ? OnTrip(
            reroute: RerouteRule(role: _role, preset: _preset),
          )
        : null,
  );

  Future<void> _start() async {
    if (!_valid || _starting) return;
    setState(() {
      _starting = true;
      _error = null;
    });
    try {
      await widget.model.create(_request);
      if (!mounted) return;
      Navigator.of(context).maybePop();
    } catch (failure) {
      if (!mounted) return;
      setState(() {
        _starting = false;
        _error = describeFailure(failure);
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final detailLabel = switch (_kind) {
      'job' => 'Job id',
      'file' => 'Path or glob',
      _ => 'Command',
    };
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      mainAxisSize: MainAxisSize.min,
      children: [
        Field(
          label: 'Agent',
          hint: 'studio for the bridge’s own session, or an agent id',
          controller: _owner,
          onChanged: (_) => setState(() {}),
        ),
        const SizedBox(height: Space.s16),
        Field(
          label: 'Name',
          hint: 'the gate',
          controller: _name,
          onChanged: (_) => setState(() => _error = null),
        ),
        const SizedBox(height: Space.s16),
        _Choice(
          label: 'Kind',
          options: const ['command', 'job', 'file'],
          selected: _kind,
          onSelected: (kind) => setState(() {
            _kind = kind;
            _detail.clear();
          }),
        ),
        const SizedBox(height: Space.s16),
        Field(
          label: detailLabel,
          hint: _kind == 'command' ? 'go test ./...' : null,
          controller: _detail,
          mono: true,
          onChanged: (_) => setState(() => _error = null),
        ),
        const SizedBox(height: Space.s16),
        Field(
          label: 'Condition',
          hint: 'change, exit_code 0, regex …, json a.b > 5',
          controller: _condition,
          mono: true,
          onChanged: (_) => setState(() {}),
        ),
        const SizedBox(height: Space.s16),
        _Choice(
          label: 'Mode',
          options: const ['once', 'repeat'],
          selected: _mode,
          onSelected: (mode) => setState(() => _mode = mode),
        ),
        const SizedBox(height: Space.s16),
        Field(
          label: 'Interval (seconds, minimum $_minInterval)',
          controller: _interval,
          onChanged: (_) => setState(() {}),
          error: _seconds >= _minInterval
              ? null
              : 'At least $_minInterval seconds.',
        ),
        const SizedBox(height: Space.s16),
        _Choice(
          label: 'On trip',
          options: const ['Notify', 'Resume the agent'],
          selected: '$_notify|$_resume',
          multi: true,
          onSelected: (choice) => setState(() {
            if (choice == 'Notify') _notify = !_notify;
            if (choice == 'Resume the agent') _resume = !_resume;
          }),
          isOn: (choice) => switch (choice) {
            'Notify' => _notify,
            _ => _resume,
          },
          unavailable: widget.choices.isEmpty
              ? 'The bridge could not list the roles and presets, so a '
                    'reroute is not offered.'
              : null,
        ),
        if (_isStudio && !widget.choices.isEmpty) ...[
          const SizedBox(height: Space.s16),
          _Choice(
            label: 'Reroute a role when this trips',
            options: const ['Reroute'],
            selected: _reroute ? 'Reroute' : '',
            onSelected: (_) => setState(() {
              _reroute = !_reroute;
              if (!_reroute) {
                _role = '';
                _preset = '';
              }
            }),
          ),
          if (_reroute) ...[
            const SizedBox(height: Space.s12),
            _Choice(
              label: 'Role',
              options: widget.choices.roles,
              selected: _role,
              onSelected: (role) => setState(() => _role = role),
            ),
            const SizedBox(height: Space.s16),
            _Choice(
              label: 'Preset',
              options: widget.choices.presets,
              selected: _preset,
              onSelected: (preset) => setState(() => _preset = preset),
            ),
            const SizedBox(height: Space.s8),
            Text(
              'Changes the active profile. Applies to agents started after '
              'it trips; the Overview offers Undo.',
              style: e.small.tint(e.muted),
            ),
          ],
        ],
        if (_error != null) ...[
          const SizedBox(height: Space.s16),
          Text(_error!, style: e.body.tint(e.err)),
        ],
        const SizedBox(height: Space.s16),
        Align(
          alignment: Alignment.centerLeft,
          child: Btn.primary(
            _starting ? 'Starting' : 'Start watch',
            onTap: _valid && !_starting ? _start : null,
          ),
        ),
      ],
    );
  }
}

/// A labelled row of choices, drawn as buttons: the selected one is primary,
/// so the choice is never colour alone.
class _Choice extends StatelessWidget {
  const _Choice({
    required this.label,
    required this.options,
    required this.selected,
    required this.onSelected,
    this.multi = false,
    this.isOn,
    this.unavailable,
  });

  final String label;
  final List<String> options;

  /// The one selected option, or — for [multi] — the key [isOn] reads.
  final String selected;
  final ValueChanged<String> onSelected;

  /// Whether the group states several independent values rather than one.
  final bool multi;
  final bool Function(String option)? isOn;

  /// Why the group cannot be used, in words. The buttons stay but do nothing.
  final String? unavailable;

  bool _chosen(String option) =>
      multi ? (isOn?.call(option) ?? false) : selected == option;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(label, style: e.small.tint(e.muted)),
        const SizedBox(height: Space.s6),
        Wrap(
          spacing: Space.s8,
          runSpacing: Space.s8,
          children: [
            for (final option in options)
              _chosen(option)
                  ? Btn.primary(
                      option,
                      onTap: unavailable == null
                          ? () => onSelected(option)
                          : null,
                    )
                  : Btn(
                      option,
                      onTap: unavailable == null
                          ? () => onSelected(option)
                          : null,
                    ),
          ],
        ),
        if (unavailable != null) ...[
          const SizedBox(height: Space.s4),
          Text(unavailable!, style: e.small.tint(e.muted)),
        ],
      ],
    );
  }
}
