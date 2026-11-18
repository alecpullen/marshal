import '../../model/fleet_store.dart' show describeFailure;
import '../../model/schedules_model.dart';
import '../kit/kit.dart';
import '../sections/section_page.dart';
import 'automations_place.dart' show AutomationsTabs;
import 'confirm_action.dart';

/// The Schedules tab of Automations (F7 decision 4): the list, a new schedule,
/// an edit, a run now, and a delete behind a confirmation.
class SchedulesTab extends StatelessWidget {
  const SchedulesTab({super.key, required this.model, this.tabs, this.state});

  final SchedulesModel model;

  /// The tab bar the section page draws; null when the tab is shown alone.
  final AutomationsTabs? tabs;

  /// The state the shell knows.
  final StateKind? state;

  @override
  Widget build(BuildContext context) {
    return SectionPage<Schedule>(
      title: 'Schedules',
      subtitle: 'Cron entries that start a recipe without you',
      model: model,
      tabs: tabs?.items,
      tabIndex: tabs?.index ?? 0,
      onTab: tabs?.onChanged,
      trailing: tabs?.trailing,
      state: state,
      createLabel: 'New schedule',
      blank: SchedulesModel.blank,
      detailTitle: (schedule) =>
          schedule.name.isEmpty ? 'New schedule' : schedule.name,
      emptyMessage: 'A schedule you add runs a recipe here, on its cron.',
      row: (schedule, onTap) => RowItem(
        name: schedule.name.isEmpty ? schedule.recipe : schedule.name,
        description: _about(schedule),
        value: _state(schedule),
        onTap: onTap,
      ),
      detail: (context, schedule) =>
          _ScheduleForm(model: model, schedule: schedule),
    );
  }

  /// Opens a new schedule's form — the one the section page's own New opens.
  static void create(BuildContext context, SchedulesModel model) {
    openSectionDetail<Schedule>(
      context,
      model: model,
      detail: (context, schedule) =>
          _ScheduleForm(model: model, schedule: schedule),
      item: SchedulesModel.blank(),
      title: 'New schedule',
    );
  }

  /// What the schedule does, in one line: its recipe and its cron.
  static String _about(Schedule schedule) {
    final recipe = schedule.recipe.isEmpty ? 'No recipe yet' : schedule.recipe;
    final cron = schedule.cron.isEmpty ? 'No cron yet' : schedule.cron;
    return '$recipe · $cron';
  }

  /// Where it stands: paused, never run, or what the last run answered.
  static String _state(Schedule schedule) {
    if (!schedule.enabled) return 'Paused';
    final result = schedule.lastResult ?? '';
    return result.isEmpty ? 'Never run' : result;
  }
}

/// A schedule's detail: its name, recipe and cron, and the two things a
/// schedule does beyond being edited — it runs now, and it is deleted.
///
/// The controllers hold what is typed; every keystroke is an edit of the
/// model's open item, so Save is the shared one and leaving asks about it.
class _ScheduleForm extends StatefulWidget {
  const _ScheduleForm({required this.model, required this.schedule});

  final SchedulesModel model;
  final Schedule schedule;

  @override
  State<_ScheduleForm> createState() => _ScheduleFormState();
}

class _ScheduleFormState extends State<_ScheduleForm> {
  late final _name = TextEditingController(text: widget.schedule.name);
  late final _recipe = TextEditingController(text: widget.schedule.recipe);
  late final _cron = TextEditingController(text: widget.schedule.cron);
  var _confirming = false;
  var _running = false;
  String? _note;
  String? _error;

  @override
  void dispose() {
    _name.dispose();
    _recipe.dispose();
    _cron.dispose();
    super.dispose();
  }

  /// The open item with what the fields hold now.
  Schedule _edited({bool? enabled}) {
    final held = widget.schedule;
    return Schedule(
      id: held.id,
      name: _name.text,
      recipe: _recipe.text,
      project: held.project,
      repoID: held.repoID,
      ref: held.ref,
      inputs: held.inputs,
      cron: _cron.text,
      enabled: enabled ?? held.enabled,
      lastRun: held.lastRun,
      lastRunAgent: held.lastRunAgent,
      lastResult: held.lastResult,
      ownerID: held.ownerID,
    );
  }

  void _changed({bool? enabled}) {
    setState(() {
      _note = null;
      _error = null;
    });
    widget.model.change(_edited(enabled: enabled));
  }

  Future<void> _run() async {
    setState(() {
      _running = true;
      _note = null;
      _error = null;
    });
    try {
      await widget.model.runNow(widget.schedule);
      if (!mounted) return;
      setState(() {
        _running = false;
        _note = 'Started. Its agent is in the sidebar while it runs.';
      });
    } catch (error) {
      if (!mounted) return;
      setState(() {
        _running = false;
        _error = describeFailure(error);
      });
    }
  }

  Future<void> _delete() async {
    setState(() => _error = null);
    try {
      await widget.model.delete(widget.schedule);
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
    final schedule = widget.schedule;
    final saved = schedule.id.isNotEmpty;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Field(
          label: 'Name',
          controller: _name,
          hint: 'Nightly gate',
          onChanged: (_) => _changed(),
        ),
        const SizedBox(height: Space.s12),
        Field(
          label: 'Recipe',
          controller: _recipe,
          hint: 'gate',
          onChanged: (_) => _changed(),
        ),
        const SizedBox(height: Space.s12),
        Field(
          label: 'Cron, in UTC',
          controller: _cron,
          hint: '0 3 * * *',
          onChanged: (_) => _changed(),
        ),
        const SizedBox(height: Space.s16),
        Wrap(
          spacing: Space.s8,
          runSpacing: Space.s8,
          children: [
            Btn(
              schedule.enabled ? 'Pause it' : 'Enable it',
              onTap: () => _changed(enabled: !schedule.enabled),
            ),
            Btn('Run it now', onTap: !saved || _running ? null : _run),
          ],
        ),
        if (_note != null) ...[
          const SizedBox(height: Space.s12),
          Text(_note!, style: e.small.tint(e.muted)),
        ],
        if (_error != null) ...[
          const SizedBox(height: Space.s12),
          Text(_error!, style: e.small.tint(e.err)),
        ],
        if (saved) ...[
          const SizedBox(height: Space.s20),
          if (_confirming)
            ConfirmAction(
              question: 'Delete ${_label(schedule)}?',
              loss:
                  'The schedule and its record of the last run are deleted. '
                  'Work it already started keeps running.',
              confirmLabel: 'Delete it',
              onConfirm: _delete,
              onCancel: () => setState(() => _confirming = false),
            )
          else
            Align(
              alignment: Alignment.centerLeft,
              child: Btn(
                'Delete this schedule',
                onTap: () => setState(() => _confirming = true),
              ),
            ),
        ],
      ],
    );
  }

  static String _label(Schedule schedule) =>
      schedule.name.isEmpty ? schedule.recipe : schedule.name;
}
