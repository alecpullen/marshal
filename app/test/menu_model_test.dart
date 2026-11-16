import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/daemon/status.dart';
import 'package:marshal_app/src/menu/host.dart';
import 'package:marshal_app/src/menu/model.dart';

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
}) =>
    DaemonStatus(
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

  test('offers stop when it is running, and start when it is stopped',
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
  });

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
    final missing =
        await draw(FakeTrayHost(), logExists: false, status: statusWith());
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
}
