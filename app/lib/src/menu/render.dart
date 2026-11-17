import 'package:tray_manager/tray_manager.dart';

import 'host.dart';
import 'model.dart';

/// RenderHost draws a [TrayMenu] in the OS tray. It is the only file allowed
/// to import `tray_manager`, so the plugin is one file that can be swapped
/// for the eventual app's own shell without touching the menu model.
///
/// The calls are the pinned plugin's (tray_manager 0.5.3, which re-exports
/// menu_base): `setIcon(path, isTemplate:)`, `setToolTip`, `setContextMenu`
/// with a `Menu` of `MenuItem`s, and `TrayListener.addListener`.
class RenderHost with TrayListener implements TrayHost {
  RenderHost({required this.onSelect, required this.log});

  /// onSelect receives a [TrayItem.id] the user clicked.
  final void Function(String id) onSelect;

  /// log writes a message somewhere the user can find it. The pinned plugin
  /// has no notification API, so this is where `notify` goes.
  final void Function(String message) log;

  bool _ready = false;

  @override
  Future<void> show(TrayMenu menu) async {
    await _ensureIcon();
    await trayManager.setContextMenu(
      Menu(items: [for (final entry in menu.entries) _item(entry)]),
    );
  }

  @override
  Future<void> notify(String message) async => log(message);

  /// The icon is set once: `assets/tray.png` is a black-on-transparent
  /// template image, so macOS themes it for the menu bar's current
  /// appearance and a Linux host draws the same alpha.
  Future<void> _ensureIcon() async {
    if (_ready) return;
    await trayManager.setIcon('assets/tray.png', isTemplate: true);
    await trayManager.setToolTip('Marshal');
    // Registering is what makes a click reach the item's onClick: the
    // plugin dispatches menu events through its listener list.
    trayManager.addListener(this);
    _ready = true;
  }

  MenuItem _item(Object entry) {
    if (entry is TraySeparator) return MenuItem.separator();
    final item = entry as TrayItem;
    // A disabled row gets no click handler at all, so the plugin cannot
    // deliver a click the app would have to ignore.
    void Function(MenuItem)? onClick;
    if (item.enabled) {
      onClick = (MenuItem clicked) => onSelect(item.id);
    }

    if (item.checkbox) {
      return MenuItem.checkbox(
        key: item.id,
        label: item.label,
        checked: item.checked,
        disabled: !item.enabled,
        onClick: onClick,
      );
    }
    return MenuItem(
      key: item.id,
      label: item.label,
      disabled: !item.enabled,
      onClick: onClick,
    );
  }
}
