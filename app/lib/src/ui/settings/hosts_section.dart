import '../../model/age.dart';
import '../../model/fleet_store.dart'
    show HostTunnel, HostView, describeFailure;
import '../../model/hosts_model.dart';
import '../kit/kit.dart';
import '../sections/section_page.dart';

/// Settings' Hosts section (F7 decision 7): every host discovery found, with
/// its kind, what this app can reach of it and when it was last heard from.
///
/// A host is named in `fleet.toml` and found by discovery. A build that can
/// run `marshal fleet` adds one here; a browser cannot, and says what it
/// reaches instead (spec D22).
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
      footer: [
        Rows([
          if (model.canAdd)
            RowItem(
              name: 'Add a host',
              description:
                  'Another machine\'s daemon, reached over SSH and added to '
                  '`fleet.toml`',
              onTap: () => _add(context),
            )
          else
            RowItem.planned(
              name: 'Add a host',
              description:
                  'Add hosts with `marshal fleet add` on the machine this '
                  'daemon runs on (D15).',
            ),
        ]),
      ],
      detail: (context, host) =>
          HostDetail(model: model, host: host, now: clock),
    );
  }

  /// Adds a host: its name and the SSH destination it is reached at. A draft
  /// that holds anything counts for the shell's guard while the sheet is open
  /// (design-system.md §9).
  Future<void> _add(BuildContext context) async {
    await Sheet.show<void>(
      context,
      Sheet(
        title: 'Add a host',
        actions: [Btn('Cancel', onTap: () => Navigator.maybePop(context))],
        child: HostForm(model: model),
      ),
    );
    model.draftChanged(null);
  }

  /// What the host is, in one line: a local daemon, a host reached over SSH,
  /// or a page's own host — and a forwarded one says where it is reached
  /// through, and where to (spec D23).
  static String _kind(HostView host) {
    if (host.kind.name == 'local') return 'A local daemon, on this machine';
    final destination = host.destination;
    final via = host.via;
    if (via != null) {
      return destination == null
          ? 'Through $via'
          : 'Through $via, over SSH to $destination';
    }
    if (destination != null) return 'Over SSH to $destination';
    return 'The daemon that served this page';
  }
}

/// What the add sheet holds (spec D6): the host's name, and the SSH
/// destination it is reached at — `you@machine`, or a Host from
/// `~/.ssh/config`.
///
/// A failed add keeps the fields and says why under them.
class HostForm extends StatefulWidget {
  const HostForm({super.key, required this.model, this.initialFailure});

  final HostsModel model;

  /// A failure shown under the fields before anything is submitted: what the
  /// caller already knows, or a fixture's.
  final String? initialFailure;

  @override
  State<HostForm> createState() => _HostFormState();
}

class _HostFormState extends State<HostForm> {
  final _name = TextEditingController();
  final _destination = TextEditingController();
  String? _failure;
  bool _adding = false;

  @override
  void initState() {
    super.initState();
    _failure = widget.initialFailure;
  }

  @override
  void dispose() {
    _name.dispose();
    _destination.dispose();
    super.dispose();
  }

  HostDraft get _draft =>
      HostDraft(name: _name.text, destination: _destination.text);

  void _changed(String _) {
    widget.model.draftChanged(_draft);
    if (_failure != null) setState(() => _failure = null);
  }

  Future<void> _add() async {
    setState(() {
      _adding = true;
      _failure = null;
    });
    try {
      await widget.model.add(_draft);
      if (!mounted) return;
      Navigator.maybePop(context);
    } catch (failure) {
      if (!mounted) return;
      setState(() {
        _adding = false;
        _failure = describeFailure(failure);
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final failure = _failure;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Field(
          label: 'Name',
          hint: 'box',
          controller: _name,
          enabled: !_adding,
          onChanged: _changed,
        ),
        const SizedBox(height: Space.s16),
        Field(
          label: 'SSH destination',
          hint: 'you@machine, or a Host from ~/.ssh/config',
          controller: _destination,
          enabled: !_adding,
          onChanged: _changed,
          onSubmitted: (_) => _add(),
        ),
        if (failure != null) ...[
          const SizedBox(height: Space.s12),
          Text(failure, style: e.small.tint(e.err)),
        ],
        const SizedBox(height: Space.s16),
        Align(
          alignment: Alignment.centerLeft,
          child: Btn.primary(
            _adding ? 'Adding' : 'Add',
            onTap: _adding ? null : _add,
          ),
        ),
      ],
    );
  }
}

/// What a host is, and what a person can do about it: a server profile is
/// removed. The local daemon is never — it is this machine's, and its profile
/// is not the Studio's to write. Nothing here signs in: the operator's proxy
/// admits the page, and a refusal is cleared by reloading it (one-daemon D13).
class HostDetail extends StatefulWidget {
  const HostDetail({
    super.key,
    required this.model,
    required this.host,
    required this.now,
  });

  final HostsModel model;
  final HostView host;
  final DateTime now;

  @override
  State<HostDetail> createState() => _HostDetailState();
}

class _HostDetailState extends State<HostDetail> {
  /// A removal's words, shown under the button that asked for it: a host
  /// that answered every request can still be refused a removal (an inline
  /// definition, a file whose mode went loose), and that refusal is the
  /// Studio's own, not the proxy's.
  String? _removeFailure;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final host = widget.host;
    final seen = host.lastContact;
    final server = host.kind.name != 'local';
    final destination = host.destination;
    final tunnel = host.tunnel;
    final removeFailure = _removeFailure;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        _Fact('Kind', HostsSection._kind(host)),
        if (destination != null) _Fact('SSH', destination),
        if (tunnel != null) _Fact('Tunnel', _tunnelWords(tunnel)),
        if (tunnel != null && tunnel.lastError.isNotEmpty)
          _Fact('Last error', tunnel.lastError),
        _Fact('Reach', host.reachWords(widget.now)),
        _Fact(
          'Last contact',
          seen == null
              ? 'Never'
              : '${relativeAge(seen, widget.now)} ago · ${_stamp(seen)}',
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
        // A browser cannot remove a host any more than it can add one, so the
        // affordance follows the runner rather than the host's kind. Only a
        // host reached over SSH has a table of its own to delete.
        if (server && widget.model.canAdd && destination != null) ...[
          const SizedBox(height: Space.s16),
          Row(children: [Btn('Remove…', onTap: () => _remove(context))]),
          if (removeFailure != null) ...[
            const SizedBox(height: Space.s12),
            Text(removeFailure, style: e.small.tint(e.err)),
          ],
        ],
      ],
    );
  }

  /// The tunnel's state in words, with how long it has been in it when the
  /// daemon said when that changed: "live", "down 4m since", and so on.
  String _tunnelWords(HostTunnel tunnel) {
    final since = tunnel.since;
    if (since == null) return tunnel.state;
    return '${tunnel.state} since '
        '${relativeAge(since, widget.now)} ago';
  }

  /// Removes the profile, after the confirmation Access asks before a revoke:
  /// it names what is lost, and what keeps running.
  Future<void> _remove(BuildContext context) async {
    final host = widget.host;
    if (_removeFailure != null) setState(() => _removeFailure = null);
    // The sheet's navigator, taken before the first await: after it the
    // widget's context may be gone, and this is the pop that closes the
    // confirmation.
    final navigator = Navigator.of(context);
    final confirmed = await Sheet.show<bool>(
      context,
      Sheet(
        title: 'Remove ${host.name}?',
        actions: [Btn('Cancel', onTap: () => Navigator.maybePop(context))],
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              'The Studio stops showing it, and its table is deleted from '
              'fleet.toml. Its daemon keeps running.',
            ),
            const SizedBox(height: Space.s16),
            Align(
              alignment: Alignment.centerLeft,
              child: Btn.primary(
                'Remove',
                onTap: () => Navigator.maybePop(context, true),
              ),
            ),
          ],
        ),
      ),
    );
    if (confirmed != true) return;
    try {
      await widget.model.remove(host.name);
    } catch (failure) {
      if (!mounted) return;
      setState(() => _removeFailure = describeFailure(failure));
      return;
    }
    if (!mounted) return;
    widget.model.cancel();
    navigator.maybePop();
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
