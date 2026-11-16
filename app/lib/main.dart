import 'src/client/contract.g.dart';
import 'src/client/host.dart';
import 'src/client/host_discovery_stub.dart'
    if (dart.library.io) 'src/client/host_discovery_io.dart';
import 'src/menu/desktop_stub.dart'
    if (dart.library.io) 'src/menu/desktop.dart';
import 'src/model/factory_store.dart';
import 'src/model/fleet_store.dart';
import 'src/model/handoff_model.dart';
import 'src/model/composer_model.dart';
import 'src/model/inbox_model.dart';
import 'src/model/new_work_model.dart';
import 'src/model/planning_model.dart';
import 'src/model/pulls_model.dart';
import 'src/model/pulls_store.dart';
import 'src/model/stack_store.dart';
import 'src/ui/kit/kit.dart';
import 'src/ui/shell/shell.dart';

/// The one entry point (F2 decision 2).
///
/// The Studio is a Flutter app on every platform, so it is always started, on
/// the fleet the platform can reach: the local daemon and every server profile
/// of a desktop, and the bridge that served the page in a browser. On a
/// desktop the window starts hidden and the menu-bar item opens it; in a
/// browser the page is the window, and [startTray] is the stub.
Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  final hosts = HostRegistry(discovery: discoverHosts);
  final store = FleetStore(hosts);
  // The work items: the one poller of the factory's list (F6 decision 14).
  final factory = FactoryStore(hosts);
  final inbox = InboxModel(
    store,
    client: (name) =>
        hosts.hosts.firstWhere((host) => host.name == name).client,
    factory: factory,
  );
  // The pull requests read the same hosts, follow the same fleet streams, and
  // post through the same clients (F5).
  final pulls = PullsModel(
    PullsStore(hosts, store),
    client: (name) =>
        hosts.hosts.firstWhere((host) => host.name == name).client,
  );
  runApp(
    EmberApp(
      home: Studio(
        store: store,
        inbox: inbox,
        pulls: pulls,
        factory: factory,
        read: hosts.discover,
        // A thread place opens its own session's transcript on the client
        // that reaches its host (F3): the same client the inbox answers with.
        threadStore: (thread) => StackStore(
          hosts.hosts.firstWhere((host) => host.name == thread.host).client,
          thread.id,
        ),
        composerFor: (thread, view) => ComposerModel.forSession(
          hosts.hosts.firstWhere((host) => host.name == thread.host).client,
          thread.id,
          view: view,
        ),
        newWorkFor: (thread) => NewWorkModel.forProject(
          store
              .projects(host: thread.host)
              .firstWhere(
                (project) => project.root == thread.project,
                orElse: () => ProjectStatus(
                  root: thread.project,
                  available: true,
                  trust: TrustState.trustNA,
                  isolation: 'available',
                ),
              ),
          hosts.hosts.firstWhere((host) => host.name == thread.host).client,
          host: thread.host,
        ),
        // A planning thread reads its planner's journal on the client that
        // reaches its host, and captures the plan for review (F6 decision 8).
        planningFor: (host, plannerID) {
          final client = hosts.hosts
              .firstWhere((owner) => owner.name == host)
              .client;
          return PlanningModel(
            plannerID: plannerID,
            events: (after) => client.plannerEvents(plannerID, after: after),
            promptPlanner: (request) =>
                client.promptPlanner(plannerID, request),
            cancelPlanner: () => client.cancelPlanner(plannerID),
            answerPlanner: (requestID, response) =>
                client.answerPlanner(plannerID, requestID, response),
            capture: (request) =>
                client.createPlannerSnapshot(plannerID, request),
            read: () => client.planner(plannerID),
          );
        },
        // The handoff review of a plan (F6 decision 8): its inherited values
        // come from the project's factory defaults, which no route reports
        // yet, so the sheet states what the project does not and the person
        // chooses the rest. Approving creates the item through the one store
        // that reads the factory.
        handoffFor: (thread, plan, spec) => HandoffModel(
          defaults: ProjectDefaults(hostID: thread.host),
          projectRoot: thread.project,
          title: thread.title,
          plan: plan,
          spec: spec,
          create: (draft) => factory.create(thread.host, draft),
        ),
      ),
    ),
  );
  factory.start();
  await startTray();
}
