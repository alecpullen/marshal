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

/// Settings (F7 decision 7): one place with ten sections, and the open one
/// drawn here. The ten are the sidebar's rows while Settings is open (F2), so
/// this place draws no list of its own: a section is a place, named by the
/// address and by the sidebar, and this is what the place shows.
///
/// A section this step does not build is drawn planned, with one line
/// saying what it will hold (decision 9); the order of the ten is fixed
/// here, so a section cannot move or go missing as the later steps land.
///
/// The appearance a person chose is the whole window's, not this place's: the
/// window is under the brightness General sets, so choosing Dark recolours
/// every screen at once, this one with them.
class SettingsPlace extends StatelessWidget {
  const SettingsPlace({
    super.key,
    required this.preferences,
    this.sections = const {},
    this.index = 0,
    this.state,
  });

  /// This window's appearance and transcript density, as the window holds
  /// them. The place draws under the window's own theme; General's section is
  /// what sets them.
  final GeneralPreferences preferences;

  /// The sections this build has. One with no builder is planned.
  final Map<SettingsSectionKind, SettingsSectionBuild> sections;

  /// The section to open: `0` is Hosts, as the design lists them.
  final int index;

  /// The state the shell knows, given to whichever section is open.
  final StateKind? state;

  /// The section this build opens: the one the place names, kept inside the
  /// ten, so a link naming a section this build does not know still opens one.
  SettingsSectionKind get _open {
    final values = SettingsSectionKind.values;
    if (index <= 0) return values.first;
    if (index >= values.length) return values.last;
    return values[index];
  }

  @override
  Widget build(BuildContext context) => _section();

  /// The open section's page, or the planned state for one this build does
  /// not have.
  Widget _section() {
    final build = sections[_open];
    if (build == null) return plannedSettingsSection(_open);
    return build(state);
  }
}
