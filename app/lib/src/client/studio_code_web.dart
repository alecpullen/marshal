import 'dart:convert';
import 'dart:js_interop';

import 'package:web/web.dart' as web;

import 'token_store_web.dart';

/// Redeems the single-use code `marshal studio` put after the page's `#`.
///
/// The daemon mints the code on its peer-checked unix socket and opens
/// `/#code=<code>`. A fragment never leaves the browser, so the code reaches
/// no request line and no access log; this posts it once to
/// /api/studio/session, which answers the bearer token, and then takes the
/// code out of the address bar.
///
/// A refused or unreachable exchange leaves the page signed out, where the
/// Studio's own token prompt is the honest answer. The URL is cleaned either
/// way: a dead code in history helps nobody.
Future<void> adoptStudioCodeFromPage() async {
  final page = Uri.base;
  final fragment = page.fragment;
  if (fragment.isEmpty) return;
  final code = Uri.splitQueryString(fragment)['code'];
  if (code == null || code.isEmpty) return;
  _stripCodeFromPage(page);

  final headers = web.Headers();
  headers.set('Content-Type', 'application/json');
  final init = web.RequestInit(
    method: 'POST',
    headers: headers,
    body: jsonEncode({'code': code}).toJS,
  );
  try {
    final response = await web.window
        .fetch('${page.origin}/api/studio/session'.toJS, init)
        .toDart;
    if (!response.ok) return;
    final body = jsonDecode((await response.text().toDart).toDart);
    if (body is Map && body['token'] is String) {
      const TokenStoreWeb().write(body['token'] as String);
    }
  } catch (_) {
    // A network failure reaches the same place as a refusal: the prompt.
  }
}

/// Drops everything from the page's `#`: the code it carried is spent either
/// way, and leaving it in history or in an address a person copies is worse
/// than an empty fragment.
void _stripCodeFromPage(Uri page) {
  final bare = page.toString().replaceFirst(RegExp(r'#.*$'), '');
  try {
    web.window.history.replaceState(null, '', bare);
  } catch (_) {
    // A sandboxed frame can refuse the rewrite; the exchange is the part that
    // matters.
  }
}
