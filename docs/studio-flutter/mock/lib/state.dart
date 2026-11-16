import 'package:flutter/widgets.dart';

import 'theme.dart';

/// Where the content area is. Five fixed places, plus a project or a thread.
enum Place { overview, inbox, pulls, project, thread, automations, library, settings }

class StudioState extends ChangeNotifier {
  Place place = Place.overview;
  String id = '';

  bool dark = true;

  bool inspectorOpen = true;
  bool inspectorWide = false;
  int inspectorTab = 0;

  final Set<String> collapsed = {};

  /// On the web a screen can be linked to directly, for example
  /// `?place=thread&id=fixtures&dark=0`.
  StudioState() {
    final q = Uri.base.queryParameters;
    place = Place.values.asNameMap()[q['place']] ?? place;
    id = q['id'] ?? id;
    dark = q['dark'] != '0';
    inspectorTab = int.tryParse(q['tab'] ?? '') ?? 0;
    inspectorWide = q['wide'] == '1';
  }

  /// Pull requests whose failure has been handed to a factory worker.
  final Set<int> fixing = {};

  S get tokens => S.resolve(dark);

  void go(Place next, [String target = '']) {
    place = next;
    id = target;
    inspectorTab = 0;
    inspectorWide = false;
    notifyListeners();
  }

  void set(VoidCallback change) {
    change();
    notifyListeners();
  }
}

class StudioScope extends InheritedNotifier<StudioState> {
  const StudioScope({super.key, required StudioState state, required super.child}) : super(notifier: state);

  static StudioState of(BuildContext context) =>
      context.dependOnInheritedWidgetOfExactType<StudioScope>()!.notifier!;
}

extension StudioContext on BuildContext {
  StudioState get studio => StudioScope.of(this);
  S get s => StudioScope.of(this).tokens;
}
