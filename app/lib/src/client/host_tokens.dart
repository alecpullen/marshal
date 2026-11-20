import 'host_tokens_stub.dart'
    if (dart.library.js_interop) 'host_tokens_web.dart'
    as impl;

// Where a relayed host's token is kept, in the build that has a store of its
// own (spec D21). A browser keeps each host's token in local storage under
// that host's own key; a native build keeps no host token at all, because the
// daemon reaches its hosts over its own tunnels. This is a file of its own
// because `main.dart` compiles for both platforms and so cannot name the
// browser store directly: the conditional import is the same one `main.dart`
// uses to pick its Studio sign-in code.

/// How this build writes a relayed host's token, or null where it has no
/// store of its own.
class HostTokenWriters {
  const HostTokenWriters({required this.store, required this.clear});

  /// Stores [token] for the relayed host [host].
  final Future<void> Function(String host, String token) store;

  /// Forgets the token stored for [host], so the host reads "token refused"
  /// again (spec D21).
  final void Function(String host) clear;
}

/// The writers this build has, or null in a native build.
HostTokenWriters? get hostTokenWriters => impl.hostTokenWriters;
