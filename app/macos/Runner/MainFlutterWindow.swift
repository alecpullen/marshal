import Cocoa
import FlutterMacOS
import ServiceManagement

/// The Studio's one window, and the menu-bar item's way into it.
///
/// This app is a menu-bar item: `LSUIElement` in Info.plist keeps it out of
/// the Dock at launch, the xib's window is ordered out before it is ever seen,
/// and the window is never destroyed — it is shown and hidden
/// (docs/studio-flutter/findings/tray-and-window.md). Closing it hides it;
/// only Quit quits.
class MainFlutterWindow: NSWindow, NSWindowDelegate {
  /// The `marshal/window` channel, kept so a native close can be reported to
  /// Dart.
  private var windowChannel: FlutterMethodChannel?

  override func awakeFromNib() {
    // This app has no window at launch: the xib's window is the Flutter
    // template's, marked visible at launch, and would otherwise open as a
    // blank 800x600 window on every launch. It is ordered out on the main
    // thread — which every window change must run on — once here, and once on
    // the next main-loop turn, in case AppKit honours the xib's
    // visible-at-launch flag only after the nib's awakeFromNib calls.
    self.orderOut(nil)
    DispatchQueue.main.async { [weak self] in self?.orderOut(nil) }

    let flutterViewController = FlutterViewController()
    let windowFrame = self.frame
    self.contentViewController = flutterViewController
    self.setFrame(windowFrame, display: true)

    // The window is ours to keep: hiding and showing it must not release it,
    // and a close the person asks for reaches `windowShouldClose` below.
    self.isReleasedWhenClosed = false
    self.delegate = self

    RegisterGeneratedPlugins(registry: flutterViewController)
    registerLaunchAtStartup(flutterViewController)
    registerWindowChannel(flutterViewController)

    super.awakeFromNib()
  }

  // MARK: - Show, hide and quit

  /// Answers the `marshal/window` channel `window_host.dart` speaks.
  ///
  /// `show` opens a hidden window and focuses a visible one, so the tray's
  /// "Open Studio" does not need to know which it is. `quit` is its own call
  /// because a closed window is not a quit.
  private func registerWindowChannel(_ controller: FlutterViewController) {
    let channel = FlutterMethodChannel(
      name: "marshal/window",
      binaryMessenger: controller.engine.binaryMessenger
    )
    // Kept so a close made with the window's own controls can be reported
    // back to Dart, which otherwise thinks the window is still open.
    self.windowChannel = channel
    channel.setMethodCallHandler { [weak self] (call: FlutterMethodCall, result: @escaping FlutterResult) in
      switch call.method {
      case "show":
        self?.showStudio()
        result(nil)
      case "hide":
        self?.hideStudio()
        result(nil)
      case "quit":
        result(nil)
        NSApp.terminate(nil)
      default:
        result(FlutterMethodNotImplemented)
      }
    }
  }

  /// Shows the window, and the Dock icon with it: the app is a normal app
  /// while a window is open, and a menu-bar item while none is.
  func showStudio() {
    NSApp.setActivationPolicy(.regular)
    if self.isMiniaturized {
      self.deminiaturize(nil)
    }
    self.makeKeyAndOrderFront(nil)
    NSApp.activate(ignoringOtherApps: true)
  }

  /// Hides the window and takes the Dock icon away. The window and its
  /// Flutter engine are kept: showing it again must be instant.
  func hideStudio() {
    self.orderOut(nil)
    NSApp.setActivationPolicy(.accessory)
  }

  /// The red button, Cmd-W and `performClose` all come here. Closing hides;
  /// it never destroys the window and never quits the app.
  func windowShouldClose(_ sender: NSWindow) -> Bool {
    hideStudio()
    // Dart is told, so its window state follows the red button and Cmd-W
    // instead of thinking a window is still in front of the person.
    windowChannel?.invokeMethod("closed", arguments: nil)
    return false
  }

  // MARK: - Launch at login

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
