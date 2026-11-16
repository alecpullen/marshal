# model/

Pure Dart: the stores and view models the Studio's screens read. Empty until
the phases that need them (F1 onwards; see
[`programme.md`](../../../../docs/studio-flutter/programme.md)).

The rule, enforced by `app/test/boundary_test.dart`: nothing here imports
Flutter (`package:flutter/…`, `dart:ui`, Material or Cupertino), `dart:io`
or a browser API. That is what lets every rule in this directory be a plain
unit test with no widget, no socket and no display.

No state-management package is used. Note that Flutter's `ChangeNotifier`
lives in `package:flutter/foundation.dart`, which this directory may not
import: a store here notifies through plain Dart, and `ui/` adapts it for
widgets. The phase that writes the first store settles the shape.
