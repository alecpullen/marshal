import 'host_tokens.dart';

// A native build offers no store and signs in to nothing: it has no browser
// store to write, so it offers none.

/// A native build has no browser store.
HostTokenWriters? get hostTokenWriters => null;
