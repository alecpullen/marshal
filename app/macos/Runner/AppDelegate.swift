import Cocoa
import FlutterMacOS

@main
class AppDelegate: FlutterAppDelegate {
  override func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
    // The tray app outlives its window: the xib's one starts hidden
    // (MainFlutterWindow.awakeFromNib), and closing it is not the user
    // quitting, because Quit lives in the menu.
    return false
  }

  /// Clicking the Dock icon of a running app whose window is hidden opens it:
  /// the window is the app, and a Dock icon with nothing behind it would be a
  /// dead end.
  override func applicationShouldHandleReopen(
    _ sender: NSApplication,
    hasVisibleWindows: Bool
  ) -> Bool {
    if !hasVisibleWindows {
      (mainFlutterWindow as? MainFlutterWindow)?.showStudio()
    }
    return true
  }

  override func applicationSupportsSecureRestorableState(_ app: NSApplication) -> Bool {
    return true
  }
}
