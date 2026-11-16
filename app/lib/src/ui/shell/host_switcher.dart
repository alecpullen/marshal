import '../../model/fleet_store.dart' show HostView;
import '../kit/kit.dart';

/// The hosts the Studio can reach, in one menu, each with what the app can
/// reach of it (design-system.md §5: a `Menu` is a popover of choices, each a
/// name and one line of explanation).
///
/// The anchor names what the sidebar is showing: every host, or the one the
/// person chose. Choosing a host narrows the sidebar's tree; the Overview and
/// the Inbox stay fleet-wide, because they are projections of the whole store
/// (F2 decision 8).
class HostSwitcher extends StatelessWidget {
  const HostSwitcher({
    super.key,
    required this.hosts,
    required this.selected,
    required this.onSelect,
    required this.now,
  });

  final List<HostView> hosts;

  /// The host the sidebar is showing, or null for all of them.
  final String? selected;

  final void Function(String? host) onSelect;

  /// What a host's age is measured against.
  final DateTime now;

  @override
  Widget build(BuildContext context) {
    final selected = this.selected;
    final label = selected ?? 'All hosts';
    return Menu(
      choices: [
        MenuChoice(
          'All hosts',
          '${_online()} of ${hosts.length} online',
          onSelect: () => onSelect(null),
        ),
        for (final host in hosts)
          MenuChoice(
            host.name,
            host.reachWords(now),
            onSelect: () => onSelect(host.name),
          ),
      ],
      anchor: (context, toggle) =>
          Btn(label, icon: Glyphs.chevron_up_chevron_down, onTap: toggle),
    );
  }

  /// How many hosts are answering now.
  int _online() => hosts.where((host) => host.live).length;
}
