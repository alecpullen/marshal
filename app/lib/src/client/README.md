# client/

The bridge client: the only code in the app that opens a socket or makes a
request. Empty until F1 ([`f1-client.md`](../../../../docs/studio-flutter/f1-client.md)).

The rule, enforced by `app/test/boundary_test.dart`:

- One interface, two transports. A file that imports `dart:io` (the unix
  socket to the local daemon, HTTPS to a server) has a name ending
  `_io.dart`; a file that imports a browser API (`dart:js_interop`,
  `package:web`, …) has a name ending `_web.dart`. No other file in the app
  may import either, apart from the tray's `daemon/cli.dart`.
- `ui/` never imports anything here. Screens read `model/`; the stores in
  `model/` are fed by this client.
- Nothing here draws a widget.

The tray does not use this client: it learns everything from
`marshal daemon status --json` and the commands in its menu, by choice
(`docs/distribution/d3-tray-app.md`).
