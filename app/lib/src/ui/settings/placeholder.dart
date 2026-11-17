import '../kit/kit.dart';
import 'settings_place.dart';

/// Why a Settings section that this step does not build is drawn planned
/// (F7 decision 9): one line saying what it will hold, never a working row.
String plannedSettingsNote(SettingsSectionKind kind) => switch (kind) {
  SettingsSectionKind.hosts => 'Hosts, discovered and read-only',
  SettingsSectionKind.environments =>
    'The environment gallery, its builds and the designer arrive with the '
        'next steps of this phase',
  SettingsSectionKind.connections =>
    'Forges with what their credential permits, and model providers with '
        'their health, arrive with the next step of this phase',
  SettingsSectionKind.models =>
    'Role to model, presets, budgets and what happens at the cap arrive with '
        'the next step of this phase',
  SettingsSectionKind.secrets =>
    'Secret references and where each is used arrive with the next step of '
        'this phase',
  SettingsSectionKind.network =>
    'Egress hosts with their rule and the pending request’s ask arrive with '
        'the next step of this phase',
  SettingsSectionKind.usage =>
    'Cost and tokens by project, role and model, the audit log and disk with '
        'prune arrive with a later step of this phase',
  SettingsSectionKind.notifications =>
    'Where Marshal reaches you, and Send a test, arrive with a later step of '
        'this phase',
  SettingsSectionKind.access =>
    'The Studio token, the Library’s MCP clients and status links arrive with '
        'a later step of this phase',
  SettingsSectionKind.general =>
    'Appearance, transcript density, the keyboard map and the local daemon',
};

/// One Settings section this step does not build: the §7 planned state, in
/// place of the section's rows.
Widget plannedSettingsSection(SettingsSectionKind kind) =>
    SingleChildScrollView(
      padding: const EdgeInsets.all(Layout.pageSide),
      child: StateMessage.planned(
        name: kind.label,
        note: plannedSettingsNote(kind),
      ),
    );
