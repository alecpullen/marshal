import 'dart:async';

import 'package:flutter/foundation.dart' show kIsWeb;

import 'src/client/bridge_client.dart';
import 'src/client/contract.g.dart';
import 'src/client/fleet_profiles.dart';
import 'src/client/host.dart';
import 'src/client/host_discovery_stub.dart'
    if (dart.library.io) 'src/client/host_discovery_io.dart';
import 'src/client/preference_store.dart';
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
import 'src/model/connect_repo_model.dart';
import 'src/model/hosts_model.dart';
import 'src/model/inbox_model.dart';
import 'src/model/inspector_state.dart';
import 'src/model/library_model.dart';
import 'src/model/models_model.dart';
import 'src/model/network_model.dart';
import 'src/model/notifications_model.dart';
import 'src/model/new_work_model.dart';
import 'src/model/per_host.dart';
import 'src/model/place_host.dart' show placeHost;
import 'src/model/planning_model.dart';
import 'src/model/project_model.dart';
import 'src/model/pulls_model.dart';
import 'src/model/pulls_store.dart';
import 'src/model/recipes_model.dart';
import 'src/model/route_translation.dart';
import 'src/model/schedules_model.dart';
import 'src/model/secrets_model.dart';
import 'src/model/section_model.dart';
import 'src/model/share_model.dart';
import 'src/model/signals_store.dart';
import 'src/model/stack_store.dart';
import 'src/model/thread_inspector_models.dart';
import 'src/model/thread_ref.dart' as threads;
import 'src/model/usage_model.dart';
import 'src/model/watches_model.dart';
import 'src/model/nav_model.dart' show projectName;
import 'src/ui/automations/automations_place.dart';
import 'src/ui/automations/bots_tab.dart';
import 'src/ui/automations/placeholder.dart';
import 'src/ui/automations/recipes_tab.dart';
import 'src/ui/automations/schedules_tab.dart';
import 'src/ui/automations/watch_form.dart';
import 'src/ui/automations/watches_tab.dart';
import 'src/ui/inspector/host.dart';
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
  final hosts = HostRegistry(discovery: discoverHosts);
  final store = FleetStore(hosts);
  // The work items: the one poller of the factory's list (F6 decision 14).
  final factory = FactoryStore(hosts);
  // This window's appearance and transcript density: Settings' General
  // section sets them, and the sections F7 built read them back. They are this
  // device's own, so a reload or a reopened window opens on the choice last
  // made: a build with a store keeps them (browser storage on the web,
  // `studio.json` on a desktop) and a test build keeps none.
  final preferenceStore = openPreferenceStore();
  final preferences = GeneralPreferences(
    appearance: preferenceStore?.read('appearance'),
    density: preferenceStore?.read('density'),
    save: preferenceStore?.write,
  );
  // Every host's tripped watches, blocked requests and held drafts (spec D4).
  // It is built after the inbox so an answer can ask for a re-read of the host
  // it answered on, and its reads are what the Overview's card and the inbox's
  // network and draft asks come from.
  late final SignalsStore signals;
  final inbox = InboxModel(
    store,
    client: (name) =>
        hosts.hosts.firstWhere((host) => host.name == name).client,
    factory: factory,
    onAnswered: (host) => signals.refresh(host),
  );
  signals = SignalsStore(hosts, inbox: inbox, now: () => DateTime.now());
  // The pull requests read the same hosts, follow the same fleet streams, and
  // post through the same clients (F5).
  final pulls = PullsModel(
    PullsStore(hosts, store),
    client: (name) =>
        hosts.hosts.firstWhere((host) => host.name == name).client,
  );
  // Automations, Library and Settings (F7): their sections read one bridge,
  // so each keeps one set of models per host, built when a person first opens
  // that host's place.
  final sections = _Sections(
    hosts: hosts,
    store: store,
    preferences: preferences,
    signals: signals,
  );
  // The appearance and density General sets are the window's, so the root is
  // what listens: every screen is drawn under the theme the person chose, not
  // only the one they chose it on.
  runApp(
    AnimatedBuilder(
      animation: preferences,
      builder: (context, _) => EmberApp(
        brightness: preferences.brightness,
        home: Studio(
          density: preferences.density,
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
          signals: signals,
          // A thread place opens its own session's transcript on the client
          // that reaches its host (F3): the same client the inbox answers with.
          // A session is opened on the bridge before it is followed, because a
          // session the project no longer holds is closed and reopened rather
          // than read (P7 decision 5); an agent's is followed as it is.
          threadStore: (thread) {
            final client = hosts.hosts
                .firstWhere((host) => host.name == thread.host)
                .client;
            return StackStore(
              client,
              thread.id,
              opener: thread.kind == threads.ThreadKind.session
                  ? () => client.openSession(thread.id)
                  : null,
            );
          },
          composerFor: (thread, view) => ComposerModel.forSession(
            hosts.hosts.firstWhere((host) => host.name == thread.host).client,
            thread.id,
            view: view,
          ),
          // The new-work dialog starts on the thread's own host and may start
          // on any other that answers with a project (spec D10, P8 plan 2
          // decision 6).
          newWorkFor: (host, project) {
            final routes = _newWorkRoutes(hosts, store);
            if (routes.isEmpty) {
              // Nothing answers with a project: the dialog still opens on the
              // thread's own host, which is what F6 built.
              return NewWorkModel.forProject(
                _newWorkProject(store, host, project),
                hosts.hosts.firstWhere((owner) => owner.name == host).client,
                host: host,
              );
            }
            // The thread's host opens it, then the first live host — which is
            // what All hosts reads — then the first host that answers with a
            // project. The shell's own scope is not plumbed into this closure,
            // so a scope naming a later host still opens on the first live
            // one.
            final scope = placeHost(store.hosts, null);
            final open = routes.containsKey(host)
                ? host
                : (scope != null && routes.containsKey(scope)
                      ? scope
                      : routes.keys.first);
            return NewWorkModel.forHosts(
              host: open,
              routes: routes,
              project: _newWorkProject(store, open, project),
            );
          },
          // Registers a repository for issue intake from the sidebar's plus
          // (F9 B1): the sheet is the Studio's, the client is the host's.
          connectRepoFor: (host) => ConnectRepoModel(
            hosts.hosts.firstWhere((owner) => owner.name == host).client,
          ),
          // A thread's Share mints a read-only status link for its agent on
          // the client that reaches its host (F7 decision 7): a running
          // thread's id is its agent id, which the route takes.
          shareFor: (host, agentId) => ShareModel(
            hosts.hosts.firstWhere((owner) => owner.name == host).client,
            agentId: agentId,
          ),
          // A thread's inspector reads the five models its tabs draw on the
          // client that reaches its host (F9 decision 4). A thread's id is
          // both its agent id and its session id, and the root is the project
          // its files are read under.
          inspectorFor: (host, thread) {
            final models = ThreadInspectorModels(
              hosts.hosts.firstWhere((owner) => owner.name == host).client,
              agentId: thread.id,
              sessionId: thread.id,
              root: thread.project,
            );
            return ThreadInspector(
              state: InspectorState(),
              body: (tab) => inspectorBody(models, tab),
              start: models.start,
              dispose: () {
                models.dispose();
              },
            );
          },
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
          handoffFor:
              (host, project, title, plan, spec, {plannerID, snapshot}) async {
                final client = hosts.hosts
                    .firstWhere((owner) => owner.name == host)
                    .client;
                var defaults = ProjectDefaults(hostID: host);
                try {
                  final settings = await client.projectSettings(project);
                  defaults = ProjectDefaults.fromSettings(
                    settings.settings,
                    hostID: host,
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
                  version = await client.factorySettingsVersion(project);
                } catch (_) {
                  version = null;
                }
                return HandoffModel(
                  defaults: defaults,
                  projectRoot: project,
                  title: title,
                  plan: plan,
                  spec: spec,
                  settingsVersion: version,
                  readSettingsVersion: (root) =>
                      client.factorySettingsVersion(root),
                  create: (draft) => factory.create(host, draft),
                  reviewedSnapshot: snapshot,
                  plannerID: plannerID,
                  handoffSnapshot: (planner, snapshotID, request) =>
                      factory.handoff(host, planner, snapshotID, request),
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
              // The Automations tab lists what this project actually has: its
              // own schedules, and the watches its agents started. The fleet
              // already knows which project an agent belongs to.
              readSchedules: client.schedules,
              readWatches: client.watches,
              projectOfAgent: (agentID) {
                for (final agent in store.agents()) {
                  if (agent.id == agentID) return agent.project;
                }
                return null;
              },
            );
          },
        ),
      ),
    ),
  );
  factory.start();
  signals.start();
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

/// The new-work routes of every host the dialog may start on: the ones that are
/// live and list at least one project, in discovery order (P8 plan 2 decision
/// 6). Each is built from that host's client and its first project, as a
/// single-host dialog always has been.
Map<String, NewWorkRoutes> _newWorkRoutes(
  HostRegistry hosts,
  FleetStore store,
) {
  final routes = <String, NewWorkRoutes>{};
  for (final owner in hosts.hosts) {
    final live = store.hosts.any(
      (host) => host.name == owner.name && host.live,
    );
    if (!live) continue;
    final projects = store.projects(host: owner.name);
    if (projects.isEmpty) continue;
    routes[owner.name] = NewWorkRoutes.forProject(projects.first, owner.client);
  }
  return routes;
}

/// The project a new-work dialog opens on, on [host]: the thread's own project
/// when that host lists it, else the first project the host lists, and — on a
/// host that lists none — the thread's project, which is what F6 opened on.
ProjectStatus _newWorkProject(FleetStore store, String host, String root) {
  final projects = store.projects(host: host);
  for (final project in projects) {
    if (project.root == root) return project;
  }
  if (projects.isNotEmpty) return projects.first;
  return ProjectStatus(
    root: root,
    available: true,
    trust: TrustState.trustNA,
    isolation: 'available',
  );
}

/// The three sectioned places F7 built, each built for the host that answers.
///
/// Each place reads one bridge, so it reads the host the Studio's switcher
/// names — the host [placeHost] resolves (spec D1). A place's models are built
/// the first time a person opens that host's place and kept for the life of the
/// window: its rows are read once, and an edit in progress survives moving
/// around the Studio. Two hosts are two sets of models, holding that host's
/// rows (spec D2: a section merges nothing across hosts).
///
/// The fleet's own host list comes from the store, so the Hosts section is
/// the one section that is not a bridge read — and it reads the same store
/// under every host, as General reads this window's preferences.
class _Sections {
  _Sections({
    required this.hosts,
    required this.store,
    required this.preferences,
    required this.signals,
  }) {
    _automations = PerHost<_Automations>(
      (host) => _Automations(
        _clientFor(host)!,
        project: _projectFor(host),
        host: host,
        now: store.now,
        // A write in a section is a change the host's signals read: the
        // Overview's card and the inbox say so again (spec D4).
        onWrote: () => signals.refresh(host),
      ),
      dispose: (models) => models.dispose(),
    );
    _library = PerHost<_Library>(
      (host) => _Library(
        _clientFor(host)!,
        project: _projectFor(host),
        projects: [
          for (final project in store.projects(host: host)) project.root,
        ],
      ),
      dispose: (models) => models.dispose(),
    );
    _settings = PerHost<_Settings>(
      (host) => _Settings(
        _clientFor(host)!,
        store: store,
        preferences: preferences,
        registry: hosts,
      ),
      dispose: (models) => models.dispose(),
    );
    // A host whose profile was removed is forgotten from all three (spec D9).
    hosts.addListener(_hostsChanged);
    _hostsChanged();
  }

  final HostRegistry hosts;
  final FleetStore store;
  final GeneralPreferences preferences;

  /// Every host's signals, re-read whenever an Automations section writes:
  /// the Overview's card and the inbox's asks are that read's (spec D4).
  final SignalsStore signals;

  late final PerHost<_Automations> _automations;
  late final PerHost<_Library> _library;
  late final PerHost<_Settings> _settings;

  /// The hosts the three caches were last built for: the ones the registry
  /// listed when it last changed.
  final _known = <String>{};

  /// The registry changed: a host it no longer lists is forgotten from all
  /// three caches, so its models are disposed and a host of that name, added
  /// again, is built afresh (spec D9).
  void _hostsChanged() {
    final listed = {for (final host in hosts.hosts) host.name};
    for (final name in _known.difference(listed)) {
      _automations.forget(name);
      _library.forget(name);
      _settings.forget(name);
    }
    _known
      ..clear()
      ..addAll(listed);
  }

  /// The client that reaches [host], or null when this fleet no longer lists
  /// it: a host can go away between the shell's build and this call.
  BridgeClient? _clientFor(String host) {
    for (final owner in hosts.hosts) {
      if (owner.name == host) return owner.client;
    }
    return null;
  }

  /// The project a section that is scoped to one starts at: [host]'s first, in
  /// the order the fleet lists them, and none where it lists none.
  String _projectFor(String host) {
    final projects = store.projects(host: host);
    return projects.isEmpty ? '' : projects.first.root;
  }

  /// The three places, each built for [host] — the host the Studio's switcher
  /// names, or the first live host under "All hosts" (spec D1).
  Widget? automations(String host, int section, StateKind? state) {
    final client = _clientFor(host);
    if (client == null) return null;
    final models = _automations.of(host);
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
      // Each tab says how many entries it holds, from the model that already
      // holds them (F9 B4). Bots counts the project's forge rows; a fleet with
      // no project draws the planned tab, which has nothing to count.
      counts: {
        0: models.project.isEmpty ? null : models.bots.items.length,
        1: models.schedules.items.length,
        2: models.watches.items.length,
        3: models.recipes.items.length,
      },
      // New automation starts the kind it names (F9 B4), in the tab that owns
      // that form. A recipe has no create of its own — one is made by copying
      // a built-in on the Recipes tab, which the Recipe choice opens.
      onNew: (tab, context) async {
        switch (tab) {
          case 1:
            SchedulesTab.create(context, models.schedules);
          case 2:
            await showWatchForm(context, models.watches);
        }
      },
    );
  }

  Widget? library(String host, int section, StateKind? state) {
    final client = _clientFor(host);
    if (client == null) return null;
    final models = _library.of(host);
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

  Widget? settings(String host, int section, StateKind? state) {
    final client = _clientFor(host);
    if (client == null) return null;
    final models = _settings.of(host);
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
              client: client,
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
    required String host,
    required DateTime now,
    void Function()? onWrote,
  }) : project = project,
       bots = BotsModel(client, project: project, now: now, onWrote: onWrote),
       schedules = SchedulesModel(client),
       watches = WatchesModel(client, host: host, now: now, onWrote: onWrote),
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

  /// Disposes the four models this set built, and nothing that is not its own.
  void dispose() {
    for (final model in <SectionModel<Object>>[
      bots,
      schedules,
      watches,
      recipes,
    ]) {
      model.dispose();
    }
  }
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

  /// Disposes the four models this set built, and nothing that is not its own.
  void dispose() {
    for (final model in <SectionModel<Object>>[
      skills,
      plugins,
      clients,
      memory,
    ]) {
      model.dispose();
    }
  }
}

/// Settings' ten sections' models (F7 decision 7), read once.
class _Settings {
  _Settings(
    BridgeClient client, {
    required this.store,
    required this.preferences,
    required HostRegistry registry,
  }) : hosts = HostsModel(
         () => store.hosts,
         // Hosts are added and removed on the daemon that keeps the tunnels —
         // the one this app runs beside, never a host reached through a
         // forward (one-daemon D15, plan 7). The first host is that daemon.
         profiles: fleetProfiles(registry.hosts.first.client),
         rediscover: registry.discover,
         forget: registry.forget,
         // The tunnels the local daemon keeps, as `GET /api/daemon` reports
         // them. A browser has no daemon of its own to ask (D7).
         tunnels: kIsWeb
             ? null
             : () async => {
                 for (final host in (await client.daemon()).hosts)
                   host.name: HostTunnel(
                     state: host.state.wire,
                     lastError: host.lastError ?? '',
                     since: host.since > 0
                         ? DateTime.fromMillisecondsSinceEpoch(
                             host.since,
                             isUtc: true,
                           )
                         : null,
                   ),
               },
       ),
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

  /// Disposes the ten models this set built, and nothing that is not its own:
  /// the store, the preferences and the host registry are the window's.
  void dispose() {
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
      model.dispose();
    }
  }
}
