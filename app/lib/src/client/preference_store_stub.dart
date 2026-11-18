import 'preference_store.dart';

// A build with no device to keep anything on: nothing is read back and nothing
// is written, so a test sees the section's defaults.

/// A build with no store of its own keeps no preferences.
PreferenceStore? openPreferenceStore() => null;
