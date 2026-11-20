import 'transport.dart';

// The stub is what a platform with no unix socket sees. The native transport is
// refused, and the caller is told why rather than getting a transport that
// cannot work.

const _reason =
    'this build has no socket: it has no unix socket transport and no '
    'browser transport';

/// Refused: a unix socket needs a native build.
Transport unixTransport(String socketPath) =>
    throw UnsupportedError('unixTransport: $_reason');
