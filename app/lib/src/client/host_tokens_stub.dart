import 'host_tokens.dart';

// A native build keeps a host's token in the OS keychain, through `marshal
// fleet login`; it has no browser store to write, so it offers none and the
// Hosts model keeps its CLI path.

/// A native build has no browser store.
HostTokenWriters? get hostTokenWriters => null;
