import Cocoa
import FlutterMacOS
import ServiceManagement

class MainFlutterWindow: NSWindow {
  override func awakeFromNib() {
    // This app is a menu-bar item and has no window (LSUIElement in
    // Info.plist): the xib's window is the Flutter template's, marked
    // visible at launch, and would otherwise open as a blank 800x600
    // window on every launch. It is ordered out on the main thread —
    // which every window change must run on — once here, and once on the
    // next main-loop turn, in case AppKit honours the xib's
    // visible-at-launch flag only after the nib's awakeFromNib calls;
    // nothing else in the app ever orders a window in.
    self.orderOut(nil)
    DispatchQueue.main.async { [weak self] in self?.orderOut(nil) }

    let flutterViewController = FlutterViewController()
    let windowFrame = self.frame
    self.contentViewController = flutterViewController
    self.setFrame(windowFrame, display: true)

    RegisterGeneratedPlugins(registry: flutterViewController)
    registerLaunchAtStartup(flutterViewController)

    super.awakeFromNib()
  }

  /// registerLaunchAtStartup answers the `launch_at_startup` method channel that
  /// `package:launch_at_startup` 0.5.1 calls on macOS. Without it the plugin's
  /// `isEnabled` throws, `main.dart` reads the login item as off, and the menu's
  /// checkbox can never turn on: the Dart side degrades quietly rather than
  /// failing, which is why the gap does not show up as an error.
  ///
  /// The plugin's README asks for the `LaunchAtLogin` Swift package and this
  /// handler. The handler is what matters, and `SMAppService.mainApp` — the API
  /// LaunchAtLogin wraps on macOS 13 and later — is in the SDK, so answering it
  /// needs no package dependency and no Xcode project change. On 10.15–12 the
  /// call reports unsupported, which `main.dart` already handles: the status
  /// item stays off and a toggle reports the reason.
  private func registerLaunchAtStartup(_ controller: FlutterViewController) {
    let channel = FlutterMethodChannel(
      name: "launch_at_startup",
      binaryMessenger: controller.engine.binaryMessenger
    )
    channel.setMethodCallHandler { (call: FlutterMethodCall, result: @escaping FlutterResult) in
      switch call.method {
      case "launchAtStartupIsEnabled":
        result(Self.launchAtStartupIsEnabled())
      case "launchAtStartupSetEnabled":
        guard let arguments = call.arguments as? [String: Any],
          let enabled = arguments["setEnabledValue"] as? Bool
        else {
          result(
            FlutterError(
              code: "bad-arguments",
              message: "launchAtStartupSetEnabled needs setEnabledValue",
              details: nil
            ))
          return
        }
        result(Self.setLaunchAtStartup(enabled))
      default:
        result(FlutterMethodNotImplemented)
      }
    }
  }

  /// The login item is the system's, not the app's: `.requiresApproval` means
  /// the user must allow it in System Settings, which is not "on" yet.
  private static func launchAtStartupIsEnabled() -> Bool {
    if #available(macOS 13.0, *) {
      return SMAppService.mainApp.status == .enabled
    }
    return false
  }

  private static func setLaunchAtStartup(_ enabled: Bool) -> FlutterError? {
    guard #available(macOS 13.0, *) else {
      return FlutterError(
        code: "unsupported",
        message: "launch at login needs macOS 13 or later",
        details: nil
      )
    }
    do {
      if enabled {
        try SMAppService.mainApp.register()
      } else {
        try SMAppService.mainApp.unregister()
      }
      return nil
    } catch {
      return FlutterError(
        code: "launch-at-login",
        message: error.localizedDescription,
        details: nil
      )
    }
  }
}
