import 'package:flutter/foundation.dart';

import '../client/contract.g.dart' show DaemonInfo;
import 'age.dart';
import 'fleet_store.dart' show describeFailure;
import 'section_model.dart';
import 'thread_view_state.dart' show Density;

/// How the window is coloured: as the system is, or as the person chose
/// (design-system.md §2).
enum Appearance { system, light, dark }

/// What General sets for this window, held where every screen reads it.
///
/// Nothing here is written to the bridge yet: the bridge's config route is
/// not mapped in this build, so the choices last as long as the app runs and
/// the section says so rather than implying they were stored.
class GeneralPreferences extends ChangeNotifier {
  Appearance _appearance = Appearance.system;
  Density _density = Density.comfortable;

  Appearance get appearance => _appearance;

  /// What a transcript opens as; a thread's own switch still overrides it.
  Density get density => _density;

  /// The brightness the whole Studio follows; null is the system's.
  Brightness? get brightness => switch (_appearance) {
    Appearance.system => null,
    Appearance.light => Brightness.light,
    Appearance.dark => Brightness.dark,
  };

  /// The theme's name in the design system's words.
  String get appearanceWords => switch (_appearance) {
    Appearance.system => 'System',
    Appearance.light => 'Light',
    Appearance.dark => 'Dark',
  };

  /// The density's name, as the thread screen names it.
  String get densityWords => switch (_density) {
    Density.outline => 'Outline',
    Density.comfortable => 'Comfortable',
    Density.full => 'Full',
  };

  void setAppearance(Appearance next) {
    if (next == _appearance) return;
    _appearance = next;
    notifyListeners();
  }

  void setDensity(Density next) {
    if (next == _density) return;
    _density = next;
    notifyListeners();
  }
}

/// One choice of a setting with a fixed set of values.
@immutable
class GeneralChoice {
  const GeneralChoice({
    required this.value,
    required this.label,
    required this.about,
  });

  /// The value as the model holds it: `Appearance.dark` is `dark`.
  final String value;

  final String label;

  /// One line saying what choosing it means.
  final String about;
}

/// One named fact a read-only setting's detail lists: a key and what it
/// does, or a fact about the daemon and its value.
@immutable
class GeneralLine {
  const GeneralLine(this.term, this.meaning);

  final String term;
  final String meaning;
}

/// One row of General: a setting, what it is for, and what it is now.
///
/// A setting with [choices] is edited by choosing one; a setting with
/// [lines] is read. Both draw [note] under the detail when they have one.
@immutable
class GeneralSetting {
  const GeneralSetting({
    required this.id,
    required this.name,
    required this.description,
    required this.value,
    this.choices = const [],
    this.lines = const [],
    this.note,
  });

  final String id;
  final String name;

  /// One line: what the setting decides.
  final String description;

  /// What it is set to now, in the design system's words.
  final String value;

  final List<GeneralChoice> choices;
  final List<GeneralLine> lines;
  final String? note;

  bool get offersChoices => choices.isNotEmpty;

  /// This setting with [value] chosen, which is the edit a save writes.
  GeneralSetting chosen(String value) => GeneralSetting(
    id: id,
    name: name,
    description: description,
    value: value,
    choices: choices,
    lines: lines,
    note: note,
  );

  /// The label of what is chosen now, for the row's value.
  String get chosenLabel {
    for (final choice in choices) {
      if (choice.value == value) return choice.label;
    }
    return value;
  }
}

/// Settings' General section (F7 decision 7): appearance, the density a
/// transcript opens as, the keyboard map, and the local daemon's state.
///
/// Everything but the daemon is this window's; the daemon's state is what
/// `GET /api/daemon` answers, the same document the tray reads.
class GeneralModel extends SectionModel<GeneralSetting> {
  GeneralModel({
    required this.preferences,
    this.keyboard = const [],
    this.daemonReader,
    DateTime Function()? now,
  }) : _now = now ?? DateTime.now;

  /// A model already holding its rows, for a test or the gallery: no read
  /// happens, and [daemon] is what the daemon row reports.
  GeneralModel.fixture({
    required this.preferences,
    this.keyboard = const [],
    DaemonInfo? daemon,
    DateTime Function()? now,
  }) : daemonReader = null,
       _now = now ?? DateTime.now {
    _reported = daemon;
    replaceItems(_settings());
  }

  final GeneralPreferences preferences;

  /// The thread screen's keys, as (key, what it does). The screen reads the
  /// same list to bind them, so this can never describe a different map.
  final List<(String, String)> keyboard;

  /// Where the daemon's state is read from, or null when nothing reads it.
  final Future<DaemonInfo> Function()? daemonReader;

  final DateTime Function() _now;

  DaemonInfo? _reported;
  String? _daemonError;

  /// The daemon's last answer, or null before it answered.
  DaemonInfo? get daemon => _reported;

  @override
  Future<List<GeneralSetting>> fetch() async {
    final read = daemonReader;
    if (read != null) {
      try {
        _reported = await read();
        _daemonError = null;
      } catch (error) {
        // A daemon that does not answer is not the section failing: the rest
        // of General still sets what it sets, and the row says what it found.
        _reported = null;
        _daemonError = describeFailure(error);
      }
    }
    return _settings();
  }

  @override
  Object keyOf(GeneralSetting item) => item.id;

  @override
  bool isNew(GeneralSetting item) => false;

  /// Writes the choice the person made and answers the row it makes: this
  /// window's preferences follow at once, and the row says what they are now.
  @override
  Future<GeneralSetting> persist(GeneralSetting item) async {
    switch (item.id) {
      case 'appearance':
        preferences.setAppearance(
          Appearance.values.firstWhere(
            (value) => value.name == item.value,
            orElse: () => preferences.appearance,
          ),
        );
      case 'density':
        preferences.setDensity(
          Density.values.firstWhere(
            (value) => value.name == item.value,
            orElse: () => preferences.density,
          ),
        );
      default:
        // The keyboard map and the daemon's state are read, not written.
        break;
    }
    return _settings().firstWhere((setting) => setting.id == item.id);
  }

  /// What is written under a setting whose choice is this window's only.
  static const windowOnly =
      'This window follows it now; the bridge stores it with the app config, '
      'which this build does not read yet.';

  List<GeneralSetting> _settings() => [
    GeneralSetting(
      id: 'appearance',
      name: 'Appearance',
      description: 'Whether the Studio is light, dark, or as the system is',
      value: preferences.appearance.name,
      choices: const [
        GeneralChoice(
          value: 'system',
          label: 'System',
          about: 'Follow the system’s light or dark setting',
        ),
        GeneralChoice(value: 'light', label: 'Light', about: 'Always light'),
        GeneralChoice(value: 'dark', label: 'Dark', about: 'Always dark'),
      ],
      note: windowOnly,
    ),
    GeneralSetting(
      id: 'density',
      name: 'Transcript density',
      description: 'How much of each row a thread opens with',
      value: preferences.density.name,
      choices: const [
        GeneralChoice(
          value: 'outline',
          label: 'Outline',
          about: 'Headlines only: a step’s first line, a tool’s name',
        ),
        GeneralChoice(
          value: 'comfortable',
          label: 'Comfortable',
          about: 'Headlines and their one line of context',
        ),
        GeneralChoice(
          value: 'full',
          label: 'Full',
          about: 'Everything a row carries',
        ),
      ],
      note:
          'A thread keeps its own switch, so this is what a thread opens with '
          'rather than a rule. $windowOnly',
    ),
    GeneralSetting(
      id: 'keyboard',
      name: 'Keyboard map',
      description: 'Every key a thread answers',
      value: '${keyboard.length} keys',
      lines: [for (final (key, what) in keyboard) GeneralLine(key, what)],
      note: 'The thread screen reads this same list to bind its keys.',
    ),
    GeneralSetting(
      id: 'daemon',
      name: 'Local daemon',
      description: 'This machine’s daemon, as the tray reads it',
      value: _daemonValue(),
      lines: _daemonLines(),
      note: _daemonNote(),
    ),
  ];

  String _daemonValue() {
    final reported = _reported;
    if (reported != null) return 'Running · v${reported.version}';
    return _daemonError == null ? 'Not reported yet' : 'Not answering';
  }

  List<GeneralLine> _daemonLines() {
    final reported = _reported;
    if (reported == null) return const [];
    final started = DateTime.fromMillisecondsSinceEpoch(reported.startedAt);
    return [
      GeneralLine('Version', reported.version),
      GeneralLine('Profile', reported.profile),
      GeneralLine('PID', '${reported.pid}'),
      GeneralLine('Started', '${relativeAge(started, _now())} ago'),
      GeneralLine('Socket', reported.socket),
      GeneralLine('Projects', '${reported.projects.length}'),
      GeneralLine('Pinned', '${reported.pinned.length}'),
    ];
  }

  String _daemonNote() {
    if (_reported != null) {
      return 'The same document `marshal daemon status --json` prints, which '
          'the tray reads.';
    }
    final note = _daemonError;
    if (note == null) return 'The daemon has not been asked yet.';
    return 'The daemon did not answer: $note';
  }
}
