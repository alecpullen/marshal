import Cocoa
import FlutterMacOS

/// MainFlutterWindow is the Studio window. The app lives as an accessory
/// (no Dock icon: LSUIElement) until the window is shown, and returns to it
/// when the window is closed. The window is never destroyed: closing hides.
class MainFlutterWindow: NSWindow, NSWindowDelegate {
  private var channel: FlutterMethodChannel?

  override func awakeFromNib() {
    // Hidden at launch, as the shipping app does (the xib's window is
    // visible by default).
    self.orderOut(nil)
    DispatchQueue.main.async { [weak self] in self?.orderOut(nil) }

    let flutterViewController = FlutterViewController()
    let windowFrame = self.frame
    self.contentViewController = flutterViewController
    self.setFrame(windowFrame, display: true)
    self.delegate = self
    self.isReleasedWhenClosed = false

    RegisterGeneratedPlugins(registry: flutterViewController)

    let channel = FlutterMethodChannel(
      name: "marshal/window", binaryMessenger: flutterViewController.engine.binaryMessenger)
    channel.setMethodCallHandler { [weak self] call, result in
      switch call.method {
      case "show": self?.showStudio(); result(nil)
      case "hide": self?.hideStudio(); result(nil)
      case "state": result(self?.stateDescription())
      default: result(FlutterMethodNotImplemented)
      }
    }
    self.channel = channel

    super.awakeFromNib()
    if CommandLine.arguments.contains("--autotest") { runAutotest() }
  }

  /// showStudio puts the app in the Dock, then shows and focuses the window.
  /// Opening and focusing are the same call.
  func showStudio() {
    NSApp.setActivationPolicy(.regular)
    if self.isMiniaturized { self.deminiaturize(nil) }
    self.makeKeyAndOrderFront(nil)
    NSApp.activate(ignoringOtherApps: true)
    channel?.invokeMethod("windowChanged", arguments: true)
    NSLog("SPIKE showStudio -> \(stateDescription())")
  }

  /// hideStudio takes the app out of the Dock and hides the window.
  func hideStudio() {
    self.orderOut(nil)
    NSApp.setActivationPolicy(.accessory)
    channel?.invokeMethod("windowChanged", arguments: false)
    NSLog("SPIKE hideStudio -> \(stateDescription())")
  }

  /// The red close button, Cmd-W and performClose all arrive here: hide, do
  /// not close, so the app survives and the Flutter engine keeps its state.
  func windowShouldClose(_ sender: NSWindow) -> Bool {
    hideStudio()
    return false
  }

  func stateDescription() -> String {
    let policy: String
    switch NSApp.activationPolicy() {
    case .regular: policy = "regular"
    case .accessory: policy = "accessory"
    default: policy = "prohibited"
    }
    return "policy=\(policy) visible=\(self.isVisible)"
  }

  /// Debug only: show / close-button / show / close-button / show, with no
  /// clicking, so a script can query the process between steps.
  private func runAutotest() {
    let steps: [(Double, String, () -> Void)] = [
      (4, "show", { self.showStudio() }),
      (10, "performClose", { self.performClose(nil) }),
      (16, "show", { self.showStudio() }),
      (22, "performClose", { self.performClose(nil) }),
      (28, "show again (focus path)", { self.showStudio(); self.showStudio() }),
    ]
    for (t, name, f) in steps {
      DispatchQueue.main.asyncAfter(deadline: .now() + t) {
        NSLog("SPIKE step \(name)")
        f()
      }
    }
  }
}
