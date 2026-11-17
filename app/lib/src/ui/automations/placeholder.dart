import '../kit/kit.dart';
import 'automations_place.dart';

/// A tab F7 plan 1 has not built yet: the planned row where it will live, with
/// one line naming the step that brings it (design-system.md §7). It is never
/// drawn as working.
class PlannedTab extends StatelessWidget {
  const PlannedTab({
    super.key,
    required this.name,
    required this.note,
    required this.tabs,
  });

  final String name;
  final String note;
  final AutomationsTabs tabs;

  @override
  Widget build(BuildContext context) {
    return PageBody(
      title: name,
      width: PageWidth.list,
      children: [
        Tabs(tabs: tabs.items, index: tabs.index, onChanged: tabs.onChanged),
        const SizedBox(height: Space.s20),
        StateMessage.planned(name: name, note: note),
      ],
    );
  }
}

/// The builder of a planned tab, for the place's tabs a later step brings.
AutomationsTab plannedAutomationsTab(String name, String note) =>
    (tabs, _) => PlannedTab(name: name, note: note, tabs: tabs);
