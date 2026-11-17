import 'package:cupertino_ui/cupertino_ui.dart' show CupertinoIcons;
import 'package:material_ui/material_ui.dart';

import 'project.dart';
import 'state.dart';
import 'widgets.dart';

/// Everything that starts work without you: one page instead of four.
class Automations extends StatefulWidget {
  const Automations({super.key});

  @override
  State<Automations> createState() => _AutomationsState();
}

class _AutomationsState extends State<Automations> {
  int tab = 0;

  static const content = [
    [
      ('PR review bot · marshal', 'Reviews PRs not opened by Marshal. 3 drafts this week, 1 waiting in the Inbox.', 'On'),
      ('PR review bot · av-portal', 'Auto-posts nits, holds anything blocking.', 'On'),
      ('CI fixer · marshal', 'Never skips, disables or quarantines a test. Limit 30m and \$2 a fix.', 'On'),
      ('CI fixer · av-portal', 'Opened PR #122 31m ago for a failing calendar test.', 'Working'),
      ('Issue intake · marshal', 'Label "marshal" on GitHub. Proposals need your confirmation.', 'Polling'),
    ],
    [
      ('Nightly dependency check', 'marshal · recipe "Upgrade dependencies and run the gate" · 02:00', 'Next in 9h'),
      ('Weekly postmortem digest', 'All projects · Mondays 09:00', 'Next in 4d'),
    ],
    [
      ('Local inference latency', 'Trips above 4s per token batch. Reroutes the implementer role to a hosted model.', 'Tripped 6m'),
      ('Gate duration · marshal', 'Trips above 10 minutes. Notifies.', 'Clear'),
      ('State volume', 'Trips above 80% full. Prunes finished workspaces.', 'Clear'),
      ('Started by agent: wait for CI on #121', 'Wakes Implementer A when checks finish.', 'Watching'),
    ],
    [
      ('Review a pull request', 'Read-only. Findings labelled blocking, should fix or nit.', '12 runs'),
      ('Fix a failing CI check', 'Fetch log, reproduce, fix, gate, PR.', '5 runs'),
      ('Upgrade dependencies and run the gate', 'One PR per ecosystem.', '9 runs'),
    ],
  ];

  @override
  Widget build(BuildContext context) {
    return PageBody(
      title: 'Automations',
      subtitle: 'Work that starts without you: bots, schedules, watches and the recipes they run',
      maxWidth: 900,
      actions: const [Btn('New automation', icon: CupertinoIcons.plus)],
      children: [
        Tabs(
          labels: const ['Bots', 'Schedules', 'Watches', 'Recipes'],
          badges: const {0: '5', 1: '2', 2: '4', 3: '3'},
          index: tab,
          onChanged: (i) => setState(() => tab = i),
        ),
        const Hairline(),
        const SizedBox(height: 18),
        SettingRows(content[tab]),
      ],
    );
  }
}

/// What agents know and can use.
class Library extends StatefulWidget {
  const Library({super.key});

  @override
  State<Library> createState() => _LibraryState();
}

class _LibraryState extends State<Library> {
  int tab = 0;

  static const content = [
    [
      ('marshal-writing-plans', 'Turns an approved design into one reviewed executable plan.', 'Built in'),
      ('tui-design', 'Layout, colour and keyboard rules for terminal interfaces.', 'User'),
      ('release-checklist', 'Steps and checks before tagging a release.', 'marshal'),
    ],
    [
      ('postgres-tools', 'Query and migration helpers.', 'v0.4 · enabled'),
      ('figma-export', 'Reads frames as reference images.', 'v1.1 · disabled'),
    ],
    [
      ('claude-code', 'Client. May submit agents; each needs your confirmation.', 'Token active'),
      ('github', 'Server. Issues and pull requests.', 'Signed in'),
      ('linear', 'Server. Needs sign-in.', 'Sign in'),
    ],
    [
      ('Tests need a C toolchain (cgo)', 'Global · learned in marshal, confirmed by 4 agents', 'High confidence'),
      ('Keep TMPDIR short on macOS', 'Project marshal · learned at step 3.2 of "socket path fix"', 'High confidence'),
      ('Stripe test keys live in the vault, never in .env', 'Environment node-web · suggested for promotion to Global', 'Review'),
    ],
  ];

  @override
  Widget build(BuildContext context) {
    return PageBody(
      title: 'Library',
      subtitle: 'What agents know and can use, across every project',
      maxWidth: 900,
      children: [
        Tabs(
          labels: const ['Skills', 'Plugins', 'MCP', 'Memory'],
          index: tab,
          onChanged: (i) => setState(() => tab = i),
        ),
        const Hairline(),
        const SizedBox(height: 18),
        SettingRows(content[tab]),
      ],
    );
  }
}

/// Infrastructure and account, as a two-pane preferences screen.
class Settings extends StatefulWidget {
  const Settings({super.key});

  @override
  State<Settings> createState() => _SettingsState();
}

class _SettingsState extends State<Settings> {
  int section = 0;

  static const sections = [
    (
      'Hosts',
      CupertinoIcons.desktopcomputer,
      'Where work runs. A host is a Marshal daemon: this Mac, or a server on your network.',
      [
        ('studio-box', 'Server · Tailscale · linux/arm64 · containers', '6 of 8 slots'),
        ('Local daemon', 'This Mac · host processes · launch at login', '1 of 2 slots'),
        ('vps-syd', 'Server · last contact 4m ago · 1 steering message queued', 'Unreachable'),
        ('Add a host', 'Install and connect, or connect an existing daemon.', ''),
      ],
    ),
    (
      'Environments',
      CupertinoIcons.cube,
      'What an agent runs in: base image, toolchains, packages, services, files, network, resources and setup.',
      [
        ('go-service', 'go 1.26, node 22 · allowlist network · 4 cpu, 8 GB · warm pool 2', 'v4 · 3 projects'),
        ('node-web', 'node 22, postgres service · allowlist network', 'v2 · 1 project'),
        ('python-data', 'python 3.13, redis service · open network', 'v1 · 1 project'),
        ('Import devcontainer.json', 'Start from what a repository already defines.', ''),
      ],
    ),
    (
      'Connections',
      CupertinoIcons.link,
      'Forges and model providers Marshal can act through.',
      [
        ('GitHub', 'alecpullen · fine-grained token · push, PRs, issues', 'Connected'),
        ('Forgejo · git.home', 'alec · SSH deploy key · push only, no PR API', 'Limited'),
        ('LM Studio', 'Local · qwen3.5-9b loaded at 32k', 'Healthy'),
        ('Anthropic', 'OAuth · refreshes in 41m', 'Healthy'),
        ('DeepSeek', 'API key in keychain', 'Healthy'),
      ],
    ),
    (
      'Models & routing',
      CupertinoIcons.sparkles,
      'Which model each role uses, and what happens at the budget cap. Saved to your user config only.',
      [
        ('Default', 'Independent agents', 'qwen3.5-9b · local'),
        ('Planner', 'Planning conversations and factory decisions', 'opus'),
        ('Implementer', 'Plan tasks and factory PRs', 'sonnet'),
        ('Reviewer', 'Plan reviews and factory reviews', 'opus'),
        ('Summaries and titles', 'Rollover, titles, narration', 'qwen3.5-9b · local'),
        ('Embedding', 'Index and retrieval', 'nomic-embed · local'),
        ('Daily budget', 'At the cap: finish the turn, then hold', r'$20'),
      ],
    ),
    (
      'Secrets',
      CupertinoIcons.lock,
      'Kept out of agent containers wherever possible: used by the daemon, or injected by the proxy.',
      [
        ('github/marshal-bot', 'Injected for api.github.com · never in the container', 'OpenBao'),
        ('stripe/test', 'Injected for api.stripe.com', 'OpenBao'),
        ('Provider keys', 'Reach the provider through the proxy', 'Keychain'),
      ],
    ),
    (
      'Network',
      CupertinoIcons.globe,
      'What agents may reach. One shared egress proxy logs every connection, per agent.',
      [
        ('proxy.golang.org', 'go-service · 1,204 requests · 3 agents', 'Allowed'),
        ('api.github.com', 'go-service, node-web · credential injected', 'Allowed'),
        ('api.stripe.com', 'node-web · asked by Booking updates 3m ago', 'Waiting on you'),
        ('pastebin.com', 'Blocked for every environment', 'Blocked'),
      ],
    ),
    (
      'Usage & audit',
      CupertinoIcons.chart_bar,
      'Cost, tokens and agent hours; every action anyone or anything took; what is on disk.',
      [
        ('This week', r'$31.40 · 14.2M tokens · 38 agent hours · 9 PRs shipped', 'Open'),
        ('By role', 'Implementer 61% · reviewer 24% · planner 15%', 'Open'),
        ('Audit log', '212 actions this week · 3 gate overrides', 'Open'),
        ('Disk', 'Mirrors 9 GB · workspaces 28 GB · images 4 GB', 'Prune'),
      ],
    ),
    (
      'Notifications',
      CupertinoIcons.bell,
      'Where Marshal reaches you when something needs you.',
      [
        ('This Mac', 'Desktop notification and bell for needs-you items', 'On'),
        ('Phone', 'Push from the mobile app', 'On'),
        ('Host drops', 'Quiet for brief drops; notify after the grace period', '5 minutes'),
        ('Signal, Element', 'Planned', 'Not available'),
      ],
    ),
    (
      'Access',
      CupertinoIcons.person_crop_circle,
      'Tokens for this Studio, MCP clients allowed to submit work, and shared status links.',
      [
        ('Studio token', 'Bearer token for studio-box', 'Rotate'),
        ('MCP clients', 'claude-code · confirmation required', '1 client'),
        ('Status links', '1 active · read-only · expires in 6 days', 'Manage'),
      ],
    ),
    (
      'General',
      CupertinoIcons.gear,
      'This app.',
      [
        ('Appearance', 'Follows the system', 'Auto'),
        ('Transcript density', 'How much of each step is shown by default', 'Comfortable'),
        ('Local daemon', 'Installed · running · launch at login', 'Manage'),
        ('Keyboard', 'Browse keys match the terminal app', 'View'),
      ],
    ),
  ];

  @override
  Widget build(BuildContext context) {
    final s = context.s;
    final current = sections[section];
    return LayoutBuilder(builder: (context, box) {
      final list = ListView(
        padding: const EdgeInsets.all(8),
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(8, 12, 8, 10),
            child: Text('Settings', style: s.text(15, weight: FontWeight.w600)),
          ),
          for (var i = 0; i < sections.length; i++)
            Tap(
              onTap: () => setState(() => section = i),
              builder: (context, hovered) => Container(
                height: 30,
                padding: const EdgeInsets.symmetric(horizontal: 8),
                decoration: BoxDecoration(
                  color: i == section ? s.selected : (hovered ? s.hover : null),
                  borderRadius: BorderRadius.circular(s.control),
                ),
                child: Row(children: [
                  Icon(sections[i].$2, size: 14, color: i == section ? s.accent : s.muted),
                  const SizedBox(width: 9),
                  Text(sections[i].$1, style: s.text(13, weight: i == section ? FontWeight.w600 : FontWeight.w400)),
                ]),
              ),
            ),
        ],
      );
      final detail = PageBody(
        title: current.$1,
        subtitle: current.$3,
        maxWidth: 760,
        children: [SettingRows(current.$4)],
      );
      if (box.maxWidth < 640) {
        return Column(children: [
          SizedBox(height: 200, child: list),
          const Hairline(),
          Expanded(child: detail),
        ]);
      }
      return Row(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
        SizedBox(width: 210, child: list),
        const Hairline(vertical: true),
        Expanded(child: detail),
      ]);
    });
  }
}
