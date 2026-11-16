import Cocoa
import FlutterMacOS

@main
class AppDelegate: FlutterAppDelegate {
  override func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
    // The tray app outlives its window: the xib's one is hidden at launch
    // (MainFlutterWindow.awakeFromNib), and closing it is not the user
    // quitting, because Quit lives in the menu.
    return false
  }

  override func applicationSupportsSecureRestorableState(_ app: NSApplication) -> Bool {
    return true
  }
}
