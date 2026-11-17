import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/daemon/cli.dart';
import 'package:marshal_app/src/daemon/status.dart';
import 'package:marshal_app/src/menu/desktop.dart';
import 'package:marshal_app/src/menu/host.dart';
import 'package:marshal_app/src/menu/model.dart';
import 'package:marshal_app/src/ui/shell/window_host.dart';

/// FakeTrayHost records what the app drew, so the menu is asserted as data.
class FakeTrayHost implements TrayHost {
  final shown = <TrayMenu>[];
  final notifications = <String>[];

  TrayMenu get last => shown.last;

  @override
  Future<void> show(TrayMenu menu) async => shown.add(menu);

  @override
  Future<void> notify(String message) async => notifications.add(message);
}

/// statusWith builds the one status these cases vary: installed, running,
/// with a daemon answering.
DaemonStatus statusWith({
  bool installed = true,
  bool running = true,
  int pid = 4321,
  DaemonInfo? daemon = const DaemonInfo(
    version: '0.9.1',
    projects: ['/home/u/src/marshal'],
    children: [],
  ),
}) => DaemonStatus(
  schemaVersion: DaemonStatus.supportedSchema,
  answering: daemon != null,
  service: ServiceState(installed: installed, running: running, pid: pid),
  linger: const LingerState(supported: true, enabled: true),
  daemon: daemon,
);

/// draw builds the menu and puts it through a host, which is how the app
/// draws it: the test asserts what the host was told to show.
Future<TrayMenu> draw(
  FakeTrayHost host, {
  bool logExists = true,
  bool launchAtLogin = false,
  bool marshalMissing = false,
  DaemonStatus? status,
  String? failure,
}) async {
  final menu = menuFor(
    appVersion: '0.9.1',
    logExists: logExists,
    launchAtLogin: launchAtLogin,
    marshalMissing: marshalMissing,
    status: status,
    failure: failure,
  );
  await host.show(menu);
  return host.last;
}

void main() {
  test('offers install when the service is missing', () async {
    final menu = await draw(
      FakeTrayHost(),
      status: statusWith(installed: false, running: false, daemon: null),
    );

    final install = menu.item('install');
    expect(install, isNotNull);
    expect(install!.enabled, isTrue);
    expect(install.label, 'Install Marshal Daemon…');
    // Exactly one of install/start/stop, never two.
    expect(menu.item('start'), isNull);
    expect(menu.item('stop'), isNull);
    expect(menu.item('status')!.label, 'Marshal — not installed');
  });

  test(
    'offers stop when it is running, and start when it is stopped',
    () async {
      final running = await draw(FakeTrayHost(), status: statusWith());
      expect(running.item('stop'), isNotNull);
      expect(running.item('start'), isNull);
      expect(running.item('install'), isNull);

      final stopped = await draw(
        FakeTrayHost(),
        status: statusWith(running: false, daemon: null),
      );
      expect(stopped.item('start'), isNotNull);
      expect(stopped.item('stop'), isNull);
      expect(stopped.item('install'), isNull);
      expect(stopped.item('status')!.label, 'Marshal — daemon stopped');
    },
  );

  test('a running service with a silent daemon says not answering', () async {
    final menu = await draw(FakeTrayHost(), status: statusWith(daemon: null));

    expect(menu.item('status')!.label, 'Marshal — daemon not answering');
    // A running daemon that does not answer is still a running daemon:
    // Stop stays offered, and Start is not.
    expect(menu.item('stop'), isNotNull);
    expect(menu.item('start'), isNull);
  });

  test('names the version skew', () async {
    final menu = await draw(
      FakeTrayHost(),
      status: statusWith(
        daemon: const DaemonInfo(
          version: '1.4.0',
          projects: ['/home/u/src/marshal'],
          children: [],
        ),
      ),
    );

    final label = menu.item('status')!.label;
    expect(label, contains('running (pid 4321)'));
    expect(label, contains('· daemon 1.4.0 (app 0.9.1)'));

    // The same build says nothing about versions: the skew line is only
    // there when there is a skew.
    final same = await draw(FakeTrayHost(), status: statusWith());
    expect(same.item('status')!.label, isNot(contains('daemon 0.9.1')));
  });

  test('disables the log item when the file is missing', () async {
    final missing = await draw(
      FakeTrayHost(),
      logExists: false,
      status: statusWith(),
    );
    final log = missing.item('log');
    expect(log, isNotNull);
    expect(log!.enabled, isFalse);

    final present = await draw(FakeTrayHost(), status: statusWith());
    expect(present.item('log')!.enabled, isTrue);
  });

  test('keeps the last status when a read fails', () async {
    final menu = await draw(
      FakeTrayHost(),
      status: statusWith(),
      failure: 'marshal daemon status said nothing',
    );

    // The status survives the failed read, and says it is stale.
    final label = menu.item('status')!.label;
    expect(label, contains('running (pid 4321)'));
    expect(label, contains('status unavailable'));

    // The actions the last good status offered are still offered.
    expect(menu.item('stop'), isNotNull);
    expect(menu.item('fleet'), isNotNull);
  });

  test('a failed first read says so instead of naming a daemon', () async {
    final menu = await draw(FakeTrayHost(), failure: 'no such file');

    expect(menu.item('status')!.label, 'Marshal — status unavailable');
    expect(menu.item('status')!.enabled, isFalse);
    // Nothing is known about the service, so no service action is offered.
    expect(menu.item('install'), isNull);
    expect(menu.item('start'), isNull);
    expect(menu.item('stop'), isNull);
  });

  test('a missing binary names itself and offers no daemon action', () async {
    final menu = await draw(FakeTrayHost(), marshalMissing: true);

    expect(menu.item('status')!.label, 'Marshal — marshal not found');
    // No daemon action over a binary that is not there (§5).
    expect(menu.item('install'), isNull);
    expect(menu.item('start'), isNull);
    expect(menu.item('stop'), isNull);
    // Fleet, the log and the login toggle are not daemon actions and stay.
    expect(menu.item('fleet'), isNotNull);
  });

  test('draws launch-at-login as a checkbox that follows the state', () async {
    final off = await draw(FakeTrayHost(), status: statusWith());
    expect(off.item('login')!.checkbox, isTrue);
    expect(off.item('login')!.checked, isFalse);

    final on = await draw(
      FakeTrayHost(),
      launchAtLogin: true,
      status: statusWith(),
    );
    expect(on.item('login')!.checked, isTrue);
  });

  test('Open Studio is the first menu item', () async {
    final menu = await draw(FakeTrayHost(), status: statusWith());

    final first = menu.entries.first;
    expect(first, isA<TrayItem>());
    expect((first as TrayItem).id, 'studio');
    expect(first.label, 'Open Studio');
    expect(first.enabled, isTrue);
  });

  test('closing the window hides and does not quit', () async {
    final host = FakeWindowHost();
    final window = WindowController(host);

    await window.show();
    expect(host.calls, ['show']);
    expect(window.open, isTrue);

    // The red button, Cmd-W and the Linux close button all mean this: the
    // window goes away and the app stays, because Quit is in the menu.
    await window.closed();
    expect(host.calls, ['show', 'hide']);
    expect(window.open, isFalse);

    // Opening it again focuses the same window; nothing was destroyed.
    await window.show();
    expect(host.calls, ['show', 'hide', 'show']);
  });

  test('quitting is its own call', () async {
    final host = FakeWindowHost();
    final window = WindowController(host);

    await window.quit();

    expect(host.calls, ['quit']);
  });

  test('the menu’s Quit quits through the window', () async {
    final cli = DaemonCli();
    final host = FakeWindowHost();
    final app = TrayApp(cli: cli, window: WindowController(host));

    await app.select('quit');

    // The window's `quit` channel method is the one that ends the app: it is
    // what both runners answer, and what leaves the process under the
    // Runner's control. Nothing in the tray exits on its own.
    expect(host.calls, ['quit']);
  });

  test('a close made natively hides the window', () async {
    final host = FakeWindowHost();
    final window = WindowController(host);
    var notified = 0;
    window.addListener(() => notified++);

    await window.show();
    expect(host.calls, ['show']);
    expect(window.open, isTrue);

    // The runner reports the red button, Cmd-W and the Linux close button on
    // the channel; the controller hides, and never quits.
    host.closeNatively();
    await pumpEventQueue();

    expect(host.calls, ['show', 'hide']);
    expect(window.open, isFalse);
    expect(
      notified,
      2,
      reason: 'open when it was shown, closed after the hide',
    );

    // It keeps answering: a window closed natively and opened again is
    // tracked the same way.
    await window.show();
    expect(window.open, isTrue);
  });

  testWidgets('the channel reports a native close to its listeners', (
    tester,
  ) async {
    final host = ChannelWindowHost();
    var closed = 0;
    host.addCloseListener(() => closed++);

    await tester.binding.defaultBinaryMessenger.handlePlatformMessage(
      'marshal/window',
      const StandardMethodCodec().encodeMethodCall(const MethodCall('closed')),
      (_) {},
    );

    expect(closed, 1);
  });
}

/// FakeWindowHost records what the app asked of its window, and reports a
/// native close when a test wants one.
class FakeWindowHost implements WindowHost {
  final calls = <String>[];
  final _closes = <VoidCallback>[];

  @override
  Future<void> show() async => calls.add('show');

  @override
  Future<void> hide() async => calls.add('hide');

  @override
  Future<void> quit() async => calls.add('quit');

  @override
  void addCloseListener(VoidCallback listener) => _closes.add(listener);

  @override
  void removeCloseListener(VoidCallback listener) => _closes.remove(listener);

  /// The window was closed with its own controls, as the runner reports it.
  void closeNatively() {
    for (final listener in List.of(_closes)) {
      listener();
    }
  }
}
