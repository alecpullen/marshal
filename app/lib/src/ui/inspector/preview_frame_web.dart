import 'dart:ui_web' as ui_web;

import 'package:flutter/widgets.dart';
import 'package:web/web.dart' as web;

/// The agent's app in the browser: an iframe on the URL the bridge issued
/// (f4-inspector.md decision 10).
Widget previewFrame(String url) {
  final id = 'marshal-preview-${url.hashCode}';
  if (_registered.add(id)) {
    ui_web.platformViewRegistry.registerViewFactory(id, (int _) {
      final frame = web.HTMLIFrameElement()..src = url;
      frame.style
        ..border = '0'
        ..width = '100%'
        ..height = '100%';
      return frame;
    });
  }
  return HtmlElementView(viewType: id);
}

/// Whether this build can frame a preview at all.
const previewFramesInApp = true;

final _registered = <String>{};
