import 'package:flutter/services.dart';
import 'package:material_ui/material_ui.dart';
import 'package:tray_manager/tray_manager.dart';

import 'src/window_channel.dart';

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  final window = WindowChannel();
  await trayManager.setIcon('assets/tray.png');
  await trayManager.setContextMenu(Menu(items: [
    MenuItem(key: 'open', label: 'Open Studio'),
    MenuItem.separator(),
    MenuItem(key: 'quit', label: 'Quit'),
  ]));
  trayManager.addListener(_Tray(window));
  runApp(const MaterialApp(
      home: Scaffold(body: Center(child: Text('Studio window')))));
}

class _Tray with TrayListener {
  _Tray(this.window);
  final WindowChannel window;

  @override
  void onTrayMenuItemClick(MenuItem item) {
    if (item.key == 'open') window.show();
    if (item.key == 'quit') SystemNavigator.pop();
  }

  @override
  void onTrayIconMouseDown() => trayManager.popUpContextMenu();
}
