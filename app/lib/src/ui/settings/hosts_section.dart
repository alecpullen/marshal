import '../../model/age.dart';
import '../../model/fleet_store.dart' show HostView;
import '../../model/hosts_model.dart';
import '../kit/kit.dart';
import '../sections/section_page.dart';

/// Settings' Hosts section (F7 decision 7): every host discovery found, with
/// its kind, what this app can reach of it and when it was last heard from.
///
/// Read-only, and adding a host is a planned row (decision 9): a host is
/// configured by the fleet profile file and found by discovery, not here.
class HostsSection extends StatelessWidget {
  const HostsSection({super.key, required this.model, this.state, this.now});

  final HostsModel model;

  /// The state the shell knows, when it knows more than the model does.
  final StateKind? state;

  /// The clock the last contacts are read against.
  final DateTime? now;

  @override
  Widget build(BuildContext context) {
    final clock = now ?? DateTime.now();
    return SectionPage<HostView>(
      title: 'Hosts',
      subtitle: 'Where Marshal runs, and what this app can reach of each',
      model: model,
      state: state,
      readOnly: true,
      emptyMessage: 'No host has answered yet.',
      row: (host, onTap) => RowItem(
        name: host.name,
        description: _kind(host),
        value: host.reachWords(clock),
        onTap: onTap,
      ),
      footer: const [
        Rows([
          RowItem.planned(
            name: 'Add a host',
            description:
                'Host onboarding is planned: a host is named in `fleet.toml` '
                'and discovery finds it, so this row lists what it finds.',
          ),
        ]),
      ],
      detail: (context, host) => _HostDetail(host: host, now: clock),
    );
  }

  /// What the host is, in one line: a local daemon, or a server profile.
  static String _kind(HostView host) => switch (host.kind.name) {
    'local' => 'A local daemon, on this machine',
    _ => 'A server profile, over TLS',
  };
}

/// What a host is: read once, since nothing here edits it.
class _HostDetail extends StatelessWidget {
  const _HostDetail({required this.host, required this.now});

  final HostView host;
  final DateTime now;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final seen = host.lastContact;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        _Fact('Kind', HostsSection._kind(host)),
        _Fact('Reach', host.reachWords(now)),
        _Fact(
          'Last contact',
          seen == null
              ? 'Never'
              : '${relativeAge(seen, now)} ago · ${_stamp(seen)}',
        ),
        const SizedBox(height: Space.s12),
        Text(
          host.live
              ? 'This host is answering: its agents, projects and asks are in '
                    'the fleet.'
              : 'This host is not answering now. Its agents and projects stay '
                    'where they were, marked with how long ago they were last '
                    'heard from.',
          style: e.small.tint(e.muted),
        ),
      ],
    );
  }

  /// The exact moment, in the tooltip's manner: a date and a time in UTC, so
  /// it reads the same wherever the window is.
  static String _stamp(DateTime at) {
    final utc = at.toUtc();
    String two(int value) => value.toString().padLeft(2, '0');
    return '${utc.year}-${two(utc.month)}-${two(utc.day)} '
        '${two(utc.hour)}:${two(utc.minute)} UTC';
  }
}

/// One named fact of a host.
class _Fact extends StatelessWidget {
  const _Fact(this.term, this.meaning);

  final String term;
  final String meaning;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Padding(
      padding: const EdgeInsets.only(bottom: Space.s12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(term, style: e.small.tint(e.muted)),
          const SizedBox(height: Space.s2),
          Text(meaning, style: e.body),
        ],
      ),
    );
  }
}
