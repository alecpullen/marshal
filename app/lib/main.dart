import 'dart:async';

import 'package:flutter/foundation.dart' show kIsWeb;

import 'src/client/bridge_client.dart';
import 'src/client/contract.g.dart';
import 'src/client/host.dart';
import 'src/client/host_discovery_stub.dart'
    if (dart.library.io) 'src/client/host_discovery_io.dart';
import 'src/client/studio_code_stub.dart'
    if (dart.library.js_interop) 'src/client/studio_code_web.dart';
import 'src/menu/desktop_stub.dart'
    if (dart.library.io) 'src/menu/desktop.dart';
import 'src/model/access_model.dart';
import 'src/model/bots_model.dart';
import 'src/model/clients_model.dart';
import 'src/model/connections_model.dart';
import 'src/model/environment_model.dart';
import 'src/model/environments_model.dart';
import 'src/model/factory_store.dart';
import 'src/model/fleet_store.dart';
import 'src/model/general_model.dart';
import 'src/model/handoff_model.dart';
import 'src/model/composer_model.dart';
import 'src/model/hosts_model.dart';
import 'src/model/inbox_model.dart';
import 'src/model/library_model.dart';
import 'src/model/models_model.dart';
import 'src/model/network_model.dart';
import 'src/model/notifications_model.dart';
import 'src/model/new_work_model.dart';
import 'src/model/planning_model.dart';
import 'src/model/project_model.dart';
import 'src/model/pulls_model.dart';
import 'src/model/pulls_store.dart';
import 'src/model/recipes_model.dart';
import 'src/model/route_translation.dart';
import 'src/model/schedules_model.dart';
import 'src/model/secrets_model.dart';
import 'src/model/section_model.dart';
import 'src/model/stack_store.dart';
import 'src/model/usage_model.dart';
import 'src/model/watches_model.dart';
import 'src/model/nav_model.dart' show projectName;
import 'src/ui/automations/automations_place.dart';
import 'src/ui/automations/bots_tab.dart';
import 'src/ui/automations/placeholder.dart';
import 'src/ui/automations/recipes_tab.dart';
import 'src/ui/automations/schedules_tab.dart';
import 'src/ui/automations/watches_tab.dart';
import 'src/ui/kit/kit.dart';
import 'src/ui/library/library_place.dart';
import 'src/ui/library/mcp_tab.dart';
import 'src/ui/library/memory_tab.dart';
import 'src/ui/library/plugins_tab.dart';
import 'src/ui/library/placeholder.dart';
import 'src/ui/library/skills_tab.dart';
import 'src/ui/settings/access_section.dart';
import 'src/ui/settings/connections_section.dart';
import 'src/ui/settings/environment_designer.dart';
import 'src/ui/settings/environments_section.dart';
import 'src/ui/settings/general_section.dart';
import 'src/ui/settings/hosts_section.dart';
import 'src/ui/settings/models_section.dart';
import 'src/ui/settings/network_section.dart';
import 'src/ui/settings/notifications_section.dart';
import 'src/ui/settings/secrets_section.dart';
import 'src/ui/settings/settings_place.dart';
import 'src/ui/settings/usage_section.dart';
import 'src/ui/shell/shell.dart';
import 'src/ui/thread/thread_screen.dart' show threadKeys;

/// The one entry point (F2 decision 2).
///
/// The Studio is a Flutter app on every platform, so it is always started, on
/// the fleet the platform can reach: the local daemon and every server profile
/// of a desktop, and the bridge that served the page in a browser. On a
/// desktop the window starts hidden and the menu-bar item opens it; in a
/// browser the page is the window, and [startTray] is the stub.
Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  // `marshal studio` opens the page with a single-use code after its `#`.
  // Redeeming it before the first request is what makes the page start signed
  // in; a native build has no page, and the stub does nothing.
  if (kIsWeb) await adoptStudioCodeFromPage();
  final hosts = HostRegistry(discovery: discoverHosts);
  final store = FleetStore(hosts);
  // The work items: the one poller of the factory's list (F6 decision 14).
  final factory = FactoryStore(hosts);
  // This window's appearance and transcript density: Settings' General
  // section sets them, and the sections F7 built read them back.
  final preferences = GeneralPreferences();
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
  // Automations, Library and Settings (F7): their sections read one bridge,
  // so they are built from the first host the fleet finds.
  final sections = _Sections(
    hosts: hosts,
    store: store,
    preferences: preferences,
  );
  runApp(
    EmberApp(
      home: AnimatedBuilder(
        animation: preferences,
        builder: (context, _) => Studio(
          store: store,
          inbox: inbox,
          pulls: pulls,
          factory: factory,
          read: hosts.discover,
          // The Overview's activity feed: the newest audit events the first
          // host that answers reports. A read that fails leaves the card
          // empty without failing the page.
          readAudit: () => hosts.hosts.first.client.audit(limit: 20),
          // The spend rail's trend and split, and the projects table's
          // column: the same usage ledger, read by project, by day and by
          // role.
          readUsage: ({String by = 'project'}) =>
              hosts.hosts.first.client.usage(by: by),
          // The place the page's own address names: an old hash route from the
          // Svelte Studio, or this build's own path (F8 decision 2). A
          // desktop has no page, so its window opens the Overview.
          startPage: kIsWeb ? startPlaceFromPage(Uri.base) : null,
          automationsFor: sections.automations,
          libraryFor: sections.library,
          settingsFor: sections.settings,
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
          // The handoff review of a plan (F6 decision 8): what it inherits is
          // the project's factory defaults, read from the project's settings —
          // the document the project page edits — when the person hands the
          // plan off. A project that cannot be read still opens the sheet on
          // the host alone, because the review must open rather than fail: its
          // fields then say the project states them not, and the person
          // chooses the rest. The action creates the item through the one store
          // that reads the factory.
          //
          // A review opened from a planning thread brings the capture it was
          // reviewed from: the sheet shows that evidence, and the draft is
          // posted through the snapshot's handoff route, so the bridge's frozen
          // bytes and verified hashes are what the item is built from. The
          // new-work dialog passes no capture and keeps the create route.
          handoffFor: (thread, plan, spec, {plannerID, snapshot}) async {
            final client = hosts.hosts
                .firstWhere((owner) => owner.name == thread.host)
                .client;
            var defaults = ProjectDefaults(hostID: thread.host);
            try {
              final settings = await client.projectSettings(thread.project);
              defaults = ProjectDefaults.fromSettings(
                settings.settings,
                hostID: thread.host,
              );
            } catch (_) {
              // The sheet opens on the host alone, stating what the project
              // does not; a settings read must not leave the plan unhanded.
            }
            // The version this review is bound to: the combined one the
            // bridge composes, so a settings change made while the review
            // is open is noticed at approval rather than frozen unread. A
            // read that fails leaves the review unbound, never blocked.
            String? version;
            try {
              version = await client.factorySettingsVersion(thread.project);
            } catch (_) {
              version = null;
            }
            return HandoffModel(
              defaults: defaults,
              projectRoot: thread.project,
              title: thread.title,
              plan: plan,
              spec: spec,
              settingsVersion: version,
              readSettingsVersion: (root) =>
                  client.factorySettingsVersion(root),
              create: (draft) => factory.create(thread.host, draft),
              reviewedSnapshot: snapshot,
              plannerID: plannerID,
              handoffSnapshot: (planner, snapshotID, request) =>
                  factory.handoff(thread.host, planner, snapshotID, request),
            );
          },
          // Answers a plan run's question, for a run opened from a project
          // page's list (F6 decision 12).
          answerRunFor: (host, agentID, answer) => hosts.hosts
              .firstWhere((owner) => owner.name == host)
              .client
              .answerRun(agentID, answer),
          // A plan run's lanes name the roles they drew, read from the same
          // session's roster (F6 decision 11). The bridge resolves an agent id
          // to its session, so the run's thread id is what the route takes.
          rosterFor: (host, agentID) => hosts.hosts
              .firstWhere((owner) => owner.name == host)
              .client
              .sessionRoster(agentID),
          // The project page (F6 decision 12): its settings are one document
          // read with the ETag that names it, so a save that names a tag the
          // bridge no longer holds is refused rather than overwriting a change
          // the person has not seen. Its runs are the runs of this project, read
          // from the same client.
          projectFor: (host, root) {
            final client = hosts.hosts
                .firstWhere((owner) => owner.name == host)
                .client;
            return ProjectModel(
              root: root,
              name: projectName(root),
              read: () => client.projectSettings(root),
              write: (settings, etag) =>
                  client.putProjectSettings(root, settings, etag: etag),
              readHealth: () => client.projectHealth(root),
              readRuns: () async => [
                for (final run in await client.runs())
                  if (run.project == root) run,
              ],
              discover: (repoID) => client.discoverRepo(repoID),
            );
          },
        ),
      ),
    ),
  );
  factory.start();
  await startTray();
}

/// The place the page at [page] names, as this build reads it (F8 decision 2).
///
/// The Studio is mounted at `/` once F8 lands, and at `/next/` during the
/// trial; either way the place is what follows the mount point.
StartPlace startPlaceFromPage(Uri page) {
  var path = page.path;
  for (final mount in const ['/next']) {
    if (path == mount || path.startsWith('$mount/')) {
      path = path.substring(mount.length);
      break;
    }
  }
  return startPlace(
    path: path,
    hash: page.hasFragment ? '#${page.fragment}' : '',
  );
}

/// The three sectioned places F7 built, built for the host that answers.
///
/// Each place reads one bridge, so it reads the first host the fleet found —
/// the local daemon where there is one. A place is built the first time a
/// person opens it and kept for the life of the window: its rows are read
/// once, and an edit in progress survives moving around the Studio.
///
/// The fleet's own host list comes from the store, so the Hosts section is
/// the one section that is not a bridge read.
class _Sections {
  _Sections({
    required this.hosts,
    required this.store,
    required this.preferences,
  });

  final HostRegistry hosts;
  final FleetStore store;
  final GeneralPreferences preferences;

  _Automations? _automations;
  _Library? _library;
  _Settings? _settings;

  /// The host whose bridge these places read, or null before discovery has
  /// answered.
  Host? get _host => hosts.hosts.isEmpty ? null : hosts.hosts.first;

  /// The project a section that is scoped to one starts at: the host's first,
  /// in the order the fleet lists them.
  String get _project {
    final host = _host;
    if (host == null) return '';
    final projects = store.projects(host: host.name);
    return projects.isEmpty ? '' : projects.first.root;
  }

  Widget? automations(int section, StateKind? state) {
    final host = _host;
    if (host == null) return null;
    final models = _automations ??= _Automations(
      host.client,
      project: _project,
      now: store.now,
    );
    return AutomationsPlace(
      bots: (tabs, state) => models.project.isEmpty
          ? PlannedTab(
              name: 'Bots',
              note:
                  'The review bot and the CI fixer are set per project, and '
                  'this fleet lists none yet.',
              tabs: tabs,
            )
          : BotsTab(model: models.bots, tabs: tabs, state: state),
      schedules: (tabs, state) =>
          SchedulesTab(model: models.schedules, tabs: tabs, state: state),
      watches: (tabs, state) =>
          WatchesTab(model: models.watches, tabs: tabs, state: state),
      recipes: (tabs, state) =>
          RecipesTab(model: models.recipes, tabs: tabs, state: state),
      tab: section,
      state: state,
    );
  }

  Widget? library(int section, StateKind? state) {
    final host = _host;
    if (host == null) return null;
    final models = _library ??= _Library(
      host.client,
      project: _project,
      projects: [
        for (final host in hosts.hosts)
          for (final project in store.projects(host: host.name)) project.root,
      ],
    );
    return LibraryPlace(
      skills: (tabs, state) =>
          SkillsTab(model: models.skills, tabs: tabs, state: state),
      plugins: (tabs, state) =>
          PluginsTab(model: models.plugins, tabs: tabs, state: state),
      mcp: (tabs, state) =>
          McpTab(model: models.clients, tabs: tabs, state: state),
      memory: (tabs, state) => models.project.isEmpty
          ? PlannedLibraryTab(
              name: 'Memory',
              note:
                  'Memory is kept per project, and this fleet lists none yet.',
              tabs: tabs,
            )
          : MemoryTab(model: models.memory, tabs: tabs, state: state),
      tab: section,
      state: state,
    );
  }

  Widget? settings(int section, StateKind? state) {
    final host = _host;
    if (host == null) return null;
    final models = _settings ??= _Settings(
      host.client,
      store: store,
      preferences: preferences,
    );
    return SettingsPlace(
      preferences: preferences,
      index: section,
      state: state,
      sections: {
        SettingsSectionKind.hosts: (state) =>
            HostsSection(model: models.hosts, state: state, now: store.now),
        SettingsSectionKind.environments: (state) => EnvironmentsSection(
          model: models.environments,
          state: state,
          designer: (row) => EnvironmentDesigner(
            model: EnvironmentModel(
              name: row.name,
              source: models.environments.loadedOf(row.name)?.source ?? '',
              client: host.client,
              published: row.version,
            ),
            state: state,
          ),
        ),
        SettingsSectionKind.connections: (state) =>
            ConnectionsSection(model: models.connections, state: state),
        SettingsSectionKind.models: (state) =>
            ModelsSection(model: models.models, state: state),
        SettingsSectionKind.secrets: (state) =>
            SecretsSection(model: models.secrets, state: state),
        SettingsSectionKind.network: (state) =>
            NetworkSection(model: models.network, state: state),
        SettingsSectionKind.usage: (state) =>
            UsageSection(model: models.usage, state: state),
        SettingsSectionKind.notifications: (state) =>
            NotificationsSection(model: models.notifications, state: state),
        SettingsSectionKind.access: (state) =>
            AccessSection(model: models.access, state: state),
        SettingsSectionKind.general: (state) =>
            GeneralSection(model: models.general, state: state),
      },
    );
  }
}

/// Automations' four sections' models (F7 decision 4), read once.
class _Automations {
  _Automations(
    BridgeClient client, {
    required String project,
    required DateTime now,
  }) : project = project,
       bots = BotsModel(client, project: project, now: now),
       schedules = SchedulesModel(client),
       watches = WatchesModel(client, now: now),
       recipes = RecipesModel(client) {
    for (final model in <SectionModel<Object>>[
      bots,
      schedules,
      watches,
      recipes,
    ]) {
      unawaited(model.load());
    }
  }

  final String project;
  final BotsModel bots;
  final SchedulesModel schedules;
  final WatchesModel watches;
  final RecipesModel recipes;
}

/// Library's four sections' models (F7 decision 5), read once.
class _Library {
  _Library(
    BridgeClient client, {
    required this.project,
    required List<String> projects,
  }) : skills = SkillsModel(client, scope: 'global'),
       plugins = PluginsModel(client, scope: 'global'),
       clients = ClientsModel(client, projects: projects),
       memory = MemoryModel(client, project: project) {
    for (final model in <SectionModel<Object>>[
      skills,
      plugins,
      clients,
      memory,
    ]) {
      unawaited(model.load());
    }
  }

  final String project;
  final SkillsModel skills;
  final PluginsModel plugins;
  final ClientsModel clients;
  final MemoryModel memory;
}

/// Settings' ten sections' models (F7 decision 7), read once.
class _Settings {
  _Settings(
    BridgeClient client, {
    required this.store,
    required this.preferences,
  }) : hosts = HostsModel(() => store.hosts),
       environments = EnvironmentsModel(client),
       connections = ConnectionsModel(client),
       models = ModelsModel(client),
       secrets = SecretsModel(client),
       network = NetworkModel(client),
       usage = UsageModel(client),
       notifications = NotificationsModel(client),
       access = AccessModel(client),
       general = GeneralModel(
         preferences: preferences,
         keyboard: [for (final key in threadKeys) (key.key, key.description)],
         daemonReader: client.daemon,
       ) {
    for (final model in <SectionModel<Object>>[
      hosts,
      environments,
      connections,
      models,
      secrets,
      network,
      usage,
      notifications,
      access,
      general,
    ]) {
      unawaited(model.load());
    }
  }

  final FleetStore store;
  final GeneralPreferences preferences;
  final HostsModel hosts;
  final EnvironmentsModel environments;
  final ConnectionsModel connections;
  final ModelsModel models;
  final SecretsModel secrets;
  final NetworkModel network;
  final UsageModel usage;
  final NotificationsModel notifications;
  final AccessModel access;
  final GeneralModel general;
}
