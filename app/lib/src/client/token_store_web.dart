import 'package:web/web.dart' as web;

/// The key the browser keeps the bridge token under.
const tokenStorageKey = 'marshal:bridge-token';

/// What a relayed host's key starts with (spec D21): the page's own key, the
/// host's name after an `@`.
const _hostPrefix = '$tokenStorageKey@';

/// The key the Svelte Studio keeps the same bridge's token under
/// (web/ui/src/lib/api.ts, `TOKEN_KEY`).
///
/// The two Studios are served by one bridge, at `/` and `/next/`, so a browser
/// already signed in to the Svelte one holds a token that opens the Flutter
/// one. Reading it is what saves pasting the token a second time; a token
/// typed into this Studio is still kept under [tokenStorageKey].
const svelteTokenStorageKey = 'marshal:token';

/// Reduces what a user actually pastes to the bare token, as the web UI does
/// (web/ui/src/lib/api.ts): a leading `Authorization:` header name and a
/// `Bearer` scheme are stripped, in either order, so a copied header line and
/// a bare value both work.
///
/// Pasting the header verbatim once sent `Authorization: Bearer Bearer <token>`
/// and every request 401'd: the bridge compares bytes. A lone `Bearer`
/// therefore reduces to nothing rather than to itself.
String normalizeToken(String raw) {
  var token = raw.trim();
  token = token.replaceFirst(
    RegExp(r'^authorization\s*:\s*', caseSensitive: false),
    '',
  );
  token = token.replaceFirst(
    RegExp(r'^bearer(?:\s+|$)', caseSensitive: false),
    '',
  );
  return token.trim();
}

/// The browser's token: typed once, kept in the browser's own storage, and
/// read here. The page never holds a token a server could read.
class TokenStoreWeb {
  /// The page's own store: the token that reaches the bridge which served the
  /// page, with the Svelte Studio's read when that key holds nothing.
  const TokenStoreWeb() : _host = null;

  /// The store for relayed host [name]: the key
  /// `marshal:bridge-token@<name>` (spec D21).
  ///
  /// A host's token is its own. The bridge that served the page is not the
  /// bridge behind the relay, so a host store reads no fallback key, and one
  /// host's token never stands in for another's.
  const TokenStoreWeb.forHost(String name) : _host = name;

  /// The relayed host this store belongs to, or null for the page's own.
  final String? _host;

  /// The key this store reads, writes and clears.
  String get _key => _host == null ? tokenStorageKey : '$_hostPrefix$_host';

  /// The stored token, normalised, or null when none is stored.
  ///
  /// This Studio's own key wins, and the Svelte Studio's is read when that key
  /// holds nothing, because one bridge serves both
  /// ([svelteTokenStorageKey]). A host's store reads its own key alone.
  String? read() {
    final storage = web.window.localStorage;
    final raw = _host == null
        ? storage.getItem(tokenStorageKey) ??
              storage.getItem(svelteTokenStorageKey)
        : storage.getItem(_key);
    if (raw == null) return null;
    final token = normalizeToken(raw);
    return token.isEmpty ? null : token;
  }

  /// Stores [raw] normalised, and answers what was stored; an empty token
  /// stores nothing and answers null.
  String? write(String raw) {
    final token = normalizeToken(raw);
    if (token.isEmpty) return null;
    web.window.localStorage.setItem(_key, token);
    return token;
  }

  /// Forgets the token.
  void clear() => web.window.localStorage.removeItem(_key);
}
