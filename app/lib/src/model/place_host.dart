import 'fleet_store.dart' show HostView;

/// The host a per-host place reads (spec D2).
///
/// A place that reads one bridge — Automations, Library, Settings (F7) — draws
/// the host the [scope] names, which is the shell's switcher. "All hosts" is
/// the scope that names none, and it is answered rather than refused: the
/// first host that is answering now, so opening a place under it reads the
/// host a person is most likely to mean.
///
/// [scope] naming a host this fleet does not list reads as "All hosts": a
/// remembered name that discovery no longer finds is not an error, and the
/// place still opens. Nothing is known at all only when nothing is listed,
/// which is the one null answer.
String? placeHost(List<HostView> hosts, String? scope) {
  if (hosts.isEmpty) return null;
  if (scope != null) {
    for (final host in hosts) {
      if (host.name == scope) return host.name;
    }
  }
  for (final host in hosts) {
    if (host.live) return host.name;
  }
  return hosts.first.name;
}
