import '../../model/general_model.dart';
import '../kit/kit.dart';
import 'placeholder.dart';

/// Settings' ten sections, in the order the design fixes (F7 decision 7).
enum SettingsSectionKind {
  hosts,
  environments,
  connections,
  models,
  secrets,
  network,
  usage,
  notifications,
  access,
  general,
}

/// What a section is called wherever it is named.
extension SettingsSectionName on SettingsSectionKind {
  String get label => switch (this) {
    SettingsSectionKind.hosts => 'Hosts',
    SettingsSectionKind.environments => 'Environments',
    SettingsSectionKind.connections => 'Connections',
    SettingsSectionKind.models => 'Models and routing',
    SettingsSectionKind.secrets => 'Secrets',
    SettingsSectionKind.network => 'Network',
    SettingsSectionKind.usage => 'Usage and audit',
    SettingsSectionKind.notifications => 'Notifications',
    SettingsSectionKind.access => 'Access',
    SettingsSectionKind.general => 'General',
  };
}

/// Builds one section's page, given the state the shell knows.
typedef SettingsSectionBuild = Widget Function(StateKind? state);

/// Settings (F7 decision 7): one place with ten sections, the sections down
/// the left and the open one beside them — stacked above it under 760.
///
/// A section this step does not build is drawn planned, with one line
/// saying what it will hold (decision 9); the order of the ten is fixed
/// here, so a section cannot move or go missing as the later steps land.
///
/// The place is also where the appearance a person chose takes effect: the
/// whole place is under [GeneralPreferences]' brightness, so choosing Dark
/// in General recolours what is on screen at once.
class SettingsPlace extends StatefulWidget {
  const SettingsPlace({
    super.key,
    required this.preferences,
    this.sections = const {},
    this.index = 0,
    this.state,
  });

  /// This window's appearance and transcript density.
  final GeneralPreferences preferences;

  /// The sections this build has. One with no builder is planned.
  final Map<SettingsSectionKind, SettingsSectionBuild> sections;

  /// The section to open: `0` is Hosts, as the design lists them.
  final int index;

  /// The state the shell knows, given to whichever section is open.
  final StateKind? state;

  @override
  State<SettingsPlace> createState() => _SettingsPlaceState();
}

class _SettingsPlaceState extends State<SettingsPlace> {
  late int _open = widget.index < 0
      ? 0
      : (widget.index >= SettingsSectionKind.values.length
            ? SettingsSectionKind.values.length - 1
            : widget.index);

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: widget.preferences,
      builder: (context, _) => EmberTheme(
        brightness: widget.preferences.brightness,
        child: Builder(
          builder: (context) {
            final narrow = MediaQuery.sizeOf(context).width < Layout.narrow;
            final content = KeyedSubtree(
              key: ValueKey(_open),
              child: _section(),
            );
            if (narrow) {
              return Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  Flexible(flex: 2, child: _list()),
                  const Hairline(),
                  Expanded(flex: 3, child: content),
                ],
              );
            }
            return Row(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                SizedBox(width: Layout.sidebar, child: _list()),
                const Hairline.vertical(),
                Expanded(child: content),
              ],
            );
          },
        ),
      ),
    );
  }

  /// The ten sections, in order, with the open one filled.
  Widget _list() {
    final e = Ember.of(context);
    return SingleChildScrollView(
      padding: const EdgeInsets.symmetric(vertical: Space.s12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(
              Space.s16,
              Space.s4,
              Space.s16,
              Space.s8,
            ),
            child: Text('Settings', style: e.heading),
          ),
          for (final (index, kind) in SettingsSectionKind.values.indexed)
            NavRow(
              label: kind.label,
              selected: index == _open,
              onTap: () => setState(() => _open = index),
            ),
        ],
      ),
    );
  }

  /// The open section's page, or the planned state for one this build does
  /// not have.
  Widget _section() {
    final kind = SettingsSectionKind.values[_open];
    final build = widget.sections[kind];
    if (build == null) return plannedSettingsSection(kind);
    return build(widget.state);
  }
}
