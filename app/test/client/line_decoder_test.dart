import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/line_decoder.dart';

/// The browser transport's chunk-to-line step, run without a browser.
///
/// `transport_web.dart` reads a body in chunks of bytes, and a chunk boundary
/// can fall inside one character's UTF-8 bytes. A decoder that decodes each
/// chunk on its own throws on the split character and kills the stream; this
/// holds the decoder to reassembling it, as `transport_io.dart` does.
void main() {
  test('a character split across chunks reassembles', () {
    final decoder = LineDecoder();

    // "hé" and its newline, split between the two bytes of "é".
    expect(decoder.add([0x68, 0xC3]), isEmpty);
    expect(decoder.add([0xA9, 0x0A]), ['hé']);
    expect(decoder.close(), isEmpty);
  });

  test('a line arrives as soon as its terminator does', () {
    final decoder = LineDecoder();
    expect(decoder.add(utf8.encode('id: 1\n')), ['id: 1']);
    expect(decoder.add(utf8.encode('data: one\n\n')), ['data: one', '']);
    expect(decoder.close(), isEmpty);
  });

  test('a last line the body did not terminate is still a line', () {
    final decoder = LineDecoder();
    expect(decoder.add(utf8.encode('id: 1\ndata: two')), ['id: 1']);
    expect(decoder.close(), ['data: two']);
  });

  test('a chunk boundary between lines loses and duplicates nothing', () {
    final decoder = LineDecoder();
    // One character per chunk is the worst case a body can arrive in.
    final bytes = utf8.encode('é\nü\n');
    final lines = <String>[];
    for (final byte in bytes) {
      lines.addAll(decoder.add([byte]));
    }
    lines.addAll(decoder.close());
    expect(lines, ['é', 'ü']);
  });

  test('a CRLF terminator leaves no carriage return', () {
    final decoder = LineDecoder();
    expect(decoder.add(utf8.encode('id: 1\r\n\r\n')), ['id: 1', '']);
    expect(decoder.close(), isEmpty);
  });
}
