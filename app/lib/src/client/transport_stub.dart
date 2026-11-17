import 'transport.dart';

// The stub is what a platform with neither dart:io nor a browser build sees.
// Both native transports are refused, and the caller is told why rather than
// getting a transport that cannot work.

const _reason =
    'this build has no socket: it has neither dart:io nor a '
    'browser transport';

/// Refused: a unix socket needs a native build.
Transport unixTransport(String socketPath) =>
    throw UnsupportedError('unixTransport: $_reason');

/// Refused: a server transport needs a native build.
Transport serverTransport(
  Uri base, {
  required TokenProvider token,
  String? caFile,
}) => throw UnsupportedError('serverTransport: $_reason');
