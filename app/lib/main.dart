import 'dart:async';

import 'package:flutter/widgets.dart';
import 'package:launch_at_startup/launch_at_startup.dart';

import 'src/daemon/cli.dart';
import 'src/daemon/status.dart';
import 'src/menu/model.dart';
import 'src/menu/render.dart';

/// appVersion is this build's version: a release passes
/// `--dart-define=APP_VERSION=<tag>`, and a developer's checkout reads `dev`.
/// The menu names it only when the daemon runs a different build.
const appVersion = String.fromEnvironment('APP_VERSION', defaultValue: 'dev');

/// Every 5 seconds, and immediately after any action: a menu is a glance, so
/// it must not show a state the daemon left minutes ago.
const pollInterval = Duration(seconds: 5);

Future<void> main() async {
  // No window: there is no MaterialApp and no route. The tray owns the
  // lifecycle, so the binding is all the app needs to talk to the plugins.
  WidgetsFlutterBinding.ensureInitialized();
  await TrayApp(cli: DaemonCli()).start();
}

/// TrayApp keeps the menu on the daemon's state and runs the commands the
/// menu offers. A failure is shown in the menu and written to the log; it is
/// never thrown at a user who has no window to read it in.
class TrayApp {
  TrayApp({required this.cli});

  final DaemonCli cli;

  late final RenderHost _host = RenderHost(onSelect: _onSelect, log: cli.log);

  DaemonStatus? _status;
  String? _failure;
  bool _marshalMissing = false;
  bool _launchAtLogin = false;

  /// start draws the menu once and then keeps it current. It returns as soon
  /// as the tray is up; the poll keeps the isolate alive.
  Future<void> start() async {
    launchAtStartup.setup(appName: 'Marshal', appPath: cli.executable);
    _launchAtLogin = await _readLaunchAtLogin();
    await _refresh();
    Timer.periodic(pollInterval, (_) => _refresh());
  }

  /// _refresh re-reads the status and redraws the menu. A failed read keeps
  /// the last good status: a daemon restarting for a second is not a reason
  /// to empty the menu. A missing binary is the exception (§5): there is no
  /// status left to keep, the menu names `marshal not found`, and the daemon
  /// actions are dropped with it.
  Future<void> _refresh() async {
    var missing = false;
    try {
      _status = await cli.status();
      _failure = null;
    } on MarshalMissingException {
      missing = true;
      _status = null;
      _failure = null;
    } catch (error) {
      _failure = error.toString();
    }
    _marshalMissing = missing;
    await _show();
  }

  Future<void> _show() async {
    await _host.show(
      menuFor(
        appVersion: appVersion,
        logExists: cli.logExists,
        launchAtLogin: _launchAtLogin,
        status: _status,
        failure: _failure,
        marshalMissing: _marshalMissing,
      ),
    );
  }

  Future<void> _onSelect(String id) async {
    switch (id) {
      case 'fleet':
        return _run(const ['fleet']);
      case 'install':
        return _run(const ['daemon', 'install']);
      case 'start':
        return _run(const ['daemon', 'start']);
      case 'stop':
        return _run(const ['daemon', 'stop']);
      case 'log':
        return cli.open(cli.logPath);
      case 'login':
        return _toggleLaunchAtLogin();
      case 'quit':
        cli.quit();
        return;
    }
  }

  /// _run runs one daemon command, reports a failure in the log, and then
  /// re-reads the status either way — a stop that failed and a stop that
  /// worked both change what the menu should say.
  Future<void> _run(List<String> args) async {
    try {
      await cli.run(args);
    } catch (error) {
      _failure = error.toString();
      await _host.notify(error.toString());
    }
    await _refresh();
  }

  Future<void> _toggleLaunchAtLogin() async {
    try {
      // Read first: the login item is the OS's, not the app's, so a change
      // made outside the app is what the toggle acts on.
      if (await launchAtStartup.isEnabled()) {
        await launchAtStartup.disable();
      } else {
        await launchAtStartup.enable();
      }
      _launchAtLogin = await launchAtStartup.isEnabled();
    } catch (error) {
      _failure = error.toString();
      await _host.notify(error.toString());
    }
    await _refresh();
  }

  Future<bool> _readLaunchAtLogin() async {
    try {
      return await launchAtStartup.isEnabled();
    } catch (_) {
      // A host without the plugin's platform channel has no login item to
      // report; the menu shows it off and the toggle reports the failure.
      return false;
    }
  }
}
