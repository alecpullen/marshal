import 'dart:convert';
import 'dart:io';

import 'preference_store.dart';

// A desktop has no page to keep anything in, so the Studio's preferences are a
// file of its own in Marshal's config directory: `studio.json`, a flat object
// of strings. It is read once, when the store is opened, and written whole on
// every change, so the file is never half a document.

/// The file this device keeps the Studio's preferences in.
const preferenceFileName = 'studio.json';

/// The desktop's store: `studio.json` in Marshal's config directory.
///
/// Marshal's user config directory is `MARSHAL_CONFIG_DIR` when it is set,
/// else `$XDG_CONFIG_HOME/marshal`, else `$HOME/.config/marshal` — the same
/// rule `internal/app/config/locations.go` applies, so the Studio keeps its
/// preferences beside `config.toml` rather than in a directory of its own.
PreferenceStore? openPreferenceStore() {
  final directory = _configDirectory(Platform.environment);
  if (directory == null) return null;
  return IoPreferenceStore(directory);
}

/// Marshal's user config directory for [environment], or null when it names
/// no home to build one under.
String? _configDirectory(Map<String, String> environment) {
  final override = environment['MARSHAL_CONFIG_DIR'];
  if (override != null && override.isNotEmpty) return override;
  final xdg = environment['XDG_CONFIG_HOME'];
  if (xdg != null && xdg.isNotEmpty) return '$xdg/marshal';
  final home = environment['HOME'];
  if (home == null || home.isEmpty) return null;
  return '$home/.config/marshal';
}

/// The `studio.json` in [directory].
class IoPreferenceStore implements PreferenceStore {
  /// Reads the file once, here: a missing or malformed file reads as empty, so
  /// a device that has never kept a preference opens on the defaults.
  IoPreferenceStore(this.directory) : _values = _read(directory);

  /// The config directory the file is in.
  final String directory;

  /// What the file held when this store was opened, and what the next write
  /// writes back with the change applied.
  final Map<String, String> _values;

  /// The file's path.
  String get path => '$directory/$preferenceFileName';

  @override
  String? read(String key) => _values[key];

  @override
  void write(String key, String value) {
    _values[key] = value;
    _save();
  }

  /// Writes the whole file, creating [directory] when it is not there yet.
  ///
  /// A directory this device will not let us write is not the person's problem
  /// at the moment they chose something: the choice stands for this visit and
  /// simply is not there on the next.
  void _save() {
    try {
      Directory(directory).createSync(recursive: true);
      File(path).writeAsStringSync(jsonEncode(_values));
    } on FileSystemException {
      // Nothing was kept; the choice that was made still applies now.
    }
  }

  /// The strings in `studio.json` under [directory], or nothing when the file
  /// is missing, unreadable, or not an object of strings.
  static Map<String, String> _read(String directory) {
    final file = File('$directory/$preferenceFileName');
    try {
      if (!file.existsSync()) return {};
      final decoded = jsonDecode(file.readAsStringSync());
      if (decoded is! Map<String, dynamic>) return {};
      return {
        for (final entry in decoded.entries)
          if (entry.value is String) entry.key: entry.value as String,
      };
    } on FileSystemException {
      return {};
    } on FormatException {
      return {};
    }
  }
}
