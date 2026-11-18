import 'preference_store_stub.dart'
    if (dart.library.js_interop) 'preference_store_web.dart'
    if (dart.library.io) 'preference_store_io.dart'
    as impl;

// Where this device keeps the Studio's own preferences (appearance and the
// density a transcript opens as). This is a file of its own because
// `main.dart` compiles for both platforms and so cannot name either store
// directly: the conditional import is the one `host_tokens.dart` uses, with
// the desktop's store added. A browser keeps them in its local storage, a
// desktop in `studio.json` in Marshal's config directory, and a test build
// keeps none.

/// The small key–value store this device keeps the Studio's preferences in.
abstract interface class PreferenceStore {
  /// What this device holds under [key], or null when it holds nothing.
  String? read(String key);

  /// Keeps [value] under [key] on this device, to be read back by a later
  /// launch.
  void write(String key, String value);
}

/// This build's store: browser storage on the web, `studio.json` in Marshal's
/// config directory on the desktop, none in a test.
PreferenceStore? openPreferenceStore() => impl.openPreferenceStore();
