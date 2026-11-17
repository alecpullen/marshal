import 'dart:convert';

/// Reassembles a streamed body's bytes into lines.
///
/// The browser transport reads the body in chunks, and a chunk boundary can
/// fall inside a character's UTF-8 bytes. Decoding each chunk on its own
/// would fail on the split character, so the bytes are fed to a decoder that
/// carries the unfinished sequence over to the next chunk (as
/// `transport_io.dart` does by transforming the response stream).
///
/// A line is handed over as soon as its terminator arrives; the line the body
/// did not terminate is handed over by [close].
class LineDecoder {
  LineDecoder() {
    _bytes = const Utf8Decoder().startChunkedConversion(
      const LineSplitter().startChunkedConversion(_LineSink(_lines)),
    );
  }

  final List<String> _lines = [];
  late final ByteConversionSink _bytes;

  /// Adds one chunk of the body and answers the lines it completed.
  List<String> add(List<int> chunk) {
    _bytes.add(chunk);
    return _drain();
  }

  /// Ends the body and answers the lines it left unterminated, the last line
  /// of the body among them.
  List<String> close() {
    _bytes.close();
    return _drain();
  }

  /// The lines collected since the last call, in order.
  List<String> _drain() {
    if (_lines.isEmpty) return const [];
    final lines = List<String>.of(_lines);
    _lines.clear();
    return lines;
  }
}

/// Collects the lines a [LineSplitter] completes.
class _LineSink implements Sink<String> {
  _LineSink(this._lines);

  final List<String> _lines;

  @override
  void add(String line) => _lines.add(line);

  @override
  void close() {}
}
