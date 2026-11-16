# model/

The stores and view models the Studio's screens read. Empty until
the phases that need them (F1 onwards; see
[`programme.md`](../../../../docs/studio-flutter/programme.md)).

The rule, enforced by `app/test/boundary_test.dart`: the only Flutter library
anything here imports is `package:flutter/foundation.dart`. Nothing imports
any other `package:flutter/…` library, `dart:ui`, Material or Cupertino,
`dart:io` or a browser API. That is what lets every rule in this directory be
a plain unit test with no widget, no socket and no display.

No state-management package is used. Stores are `ChangeNotifier`s, which live
in `package:flutter/foundation.dart`, and `ui/` hands them down to widgets
(decided 2026-11-17).
