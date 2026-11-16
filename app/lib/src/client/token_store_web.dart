import 'package:web/web.dart' as web;

/// The key the browser keeps the bridge token under.
const tokenStorageKey = 'marshal:bridge-token';

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
  const TokenStoreWeb();

  /// The stored token, normalised, or null when none is stored.
  String? read() {
    final raw = web.window.localStorage.getItem(tokenStorageKey);
    if (raw == null) return null;
    final token = normalizeToken(raw);
    return token.isEmpty ? null : token;
  }

  /// Stores [raw] normalised, and answers what was stored; an empty token
  /// stores nothing and answers null.
  String? write(String raw) {
    final token = normalizeToken(raw);
    if (token.isEmpty) return null;
    web.window.localStorage.setItem(tokenStorageKey, token);
    return token;
  }

  /// Forgets the token.
  void clear() => web.window.localStorage.removeItem(tokenStorageKey);
}
