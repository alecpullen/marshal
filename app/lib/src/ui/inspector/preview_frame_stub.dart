import 'package:flutter/widgets.dart';

/// A preview frame where the platform has none: a desktop window cannot show
/// the agent's app, so the tab lists the ports and says why
/// (f4-inspector.md decision 10).
Widget previewFrame(String url) => const SizedBox.shrink();

/// Whether this build can frame a preview at all.
const previewFramesInApp = false;
