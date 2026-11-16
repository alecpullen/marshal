import 'dart:convert';
import 'dart:typed_data';

/// Decodes one bridge SSE `data:` payload: {"data": base64} or {"exit": n}.
class TerminalEvent {
  TerminalEvent.output(this.bytes) : exit = null;
  TerminalEvent.exited(this.exit) : bytes = null;
  final Uint8List? bytes;
  final int? exit;
}

TerminalEvent? decodeTerminalEvent(String json) {
  try {
    final v = jsonDecode(json) as Map<String, dynamic>;
    if (v['data'] is String) {
      return TerminalEvent.output(base64Decode(v['data'] as String));
    }
    if (v['exit'] is int) return TerminalEvent.exited(v['exit'] as int);
  } catch (_) {}
  return null;
}

/// Input as the bridge's /input route wants it: base64 of the UTF-8 text,
/// in pieces of at most 16 KiB (the web client's CHUNK; bridge cap is 64 KiB).
List<String> inputBodies(String text, {int chunk = 16 * 1024}) {
  final bytes = utf8.encode(text);
  return [
    for (var i = 0; i < bytes.length; i += chunk)
      base64Encode(bytes.sublist(i, i + chunk > bytes.length ? bytes.length : i + chunk)),
  ];
}
