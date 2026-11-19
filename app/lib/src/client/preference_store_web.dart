import 'package:web/web.dart' as web;

import 'preference_store.dart';

// A browser's only store is its own local storage, under one `marshal:studio:`
// prefix for everything this Studio keeps: one key per preference, and no
// other page's keys touched. Storage can be refused — private browsing, a
// policy, a full quota — so every read and write is wrapped: a refused read
// is nothing stored, and a refused write keeps nothing rather than failing
// the choice just made.

/// What a Studio preference's key starts with in local storage.
const _prefix = 'marshal:studio:';

/// The browser's store: `marshal:studio:<key>` in this page's local storage.
class WebPreferenceStore implements PreferenceStore {
  const WebPreferenceStore();

  /// The key [key] is kept under.
  String _storageKey(String key) => '$_prefix$key';

  @override
  String? read(String key) {
    try {
      return web.window.localStorage.getItem(_storageKey(key));
    } catch (_) {
      // Storage this page may not read holds nothing as far as it can tell.
      return null;
    }
  }

  @override
  void write(String key, String value) {
    try {
      web.window.localStorage.setItem(_storageKey(key), value);
    } catch (_) {
      // Storage this page may not write keeps nothing: the choice still
      // stands for this visit, and the next one opens on the default.
    }
  }
}

/// The browser's store.
PreferenceStore? openPreferenceStore() => const WebPreferenceStore();
