/// One value per host, built on first use and kept (decision 4).
///
/// Automations, Library and Settings each read one host's bridge, so each
/// keeps one set of models per host: opening a place for a host builds its
/// models the first time and opening it again reads the same models, keeping
/// the rows they read and any edit in progress (F7 decision 3).
///
/// A host that leaves the fleet can be forgotten, which disposes its value and
/// drops it, so the next read of that host builds a fresh one. Nothing here
/// watches the fleet: the caller says which hosts are gone.
///
/// A disposed cache must not be used again: it holds nothing, and a value built
/// after it is one nothing would dispose.
class PerHost<T> {
  PerHost(T Function(String host) build, {void Function(T value)? dispose})
    : _build = build,
      _release = dispose;

  /// Builds one host's value, which is where a set of models is made.
  final T Function(String host) _build;

  /// Releases one host's value; absent when there is nothing to release.
  final void Function(T value)? _release;

  final _values = <String, T>{};

  /// [host]'s value, built the first time it is asked for and kept after.
  T of(String host) => _values.putIfAbsent(host, () => _build(host));

  /// Whether [host] already has a value.
  bool holds(String host) => _values.containsKey(host);

  /// Disposes [host]'s value and drops it, leaving every other host alone.
  void forget(String host) {
    if (!_values.containsKey(host)) return;
    final value = _values.remove(host) as T;
    _release?.call(value);
  }

  /// Disposes every value and clears the cache. A second call does nothing.
  void dispose() {
    final values = _values.values.toList(growable: false);
    _values.clear();
    for (final value in values) {
      _release?.call(value);
    }
  }
}
