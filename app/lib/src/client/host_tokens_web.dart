import 'host_tokens.dart';
import 'token_store_web.dart';

// A browser's only store is its own local storage, and a relayed host's token
// lives under that host's own key: one host's token never stands in for
// another's (spec D21).

/// The browser's writers: the host's own key, written and cleared.
HostTokenWriters? get hostTokenWriters =>
    const HostTokenWriters(store: _store, clear: _clear);

Future<void> _store(String host, String token) async =>
    TokenStoreWeb.forHost(host).write(token);

void _clear(String host) => TokenStoreWeb.forHost(host).clear();
