import '../../model/age.dart';
import '../../model/fleet_store.dart' show HostView, describeFailure;
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
                  'A server profile, added to `fleet.toml` and signed in to',
              onTap: () => _add(context),
            )
          else
            RowItem.planned(
              name: 'Add a host',
              description:
                  'A browser reaches the hosts the bridge that served it '
                  'relays, so a host is added from the desktop app.',
            ),
        ]),
      ],
      detail: (context, host) =>
          HostDetail(model: model, host: host, now: clock),
    );
  }

  /// Adds a server profile: its name, its URL, the certificates it is trusted
  /// with and its token. A draft that holds anything counts for the shell's
  /// guard while the sheet is open (design-system.md §9).
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

  /// What the host is, in one line: a local daemon, or a server profile.
  static String _kind(HostView host) => switch (host.kind.name) {
    'local' => 'A local daemon, on this machine',
    _ => 'A server profile, over TLS',
  };
}

/// What the add sheet holds (spec D6): a server's name and URL, the file whose
/// certificates it is trusted with, and the token to reach it with.
///
/// The token is written and never read back, as Secrets' new value is. A
/// failed add keeps the fields and says why under them; the sheet closes as
/// added, because the profile is there and only the token is not.
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
  final _url = TextEditingController();
  final _caFile = TextEditingController();
  final _token = TextEditingController();
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
    _url.dispose();
    _caFile.dispose();
    _token.dispose();
    super.dispose();
  }

  HostDraft get _draft => HostDraft(
    name: _name.text,
    url: _url.text,
    caFile: _caFile.text,
    token: _token.text,
  );

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
          hint: 'studio',
          controller: _name,
          enabled: !_adding,
          onChanged: _changed,
        ),
        const SizedBox(height: Space.s16),
        Field(
          label: 'URL',
          hint: 'https://studio.example.com',
          controller: _url,
          enabled: !_adding,
          onChanged: _changed,
          onSubmitted: (_) => _add(),
        ),
        const SizedBox(height: Space.s16),
        Field(
          label: 'CA file',
          hint: 'The certificates this host is trusted with',
          controller: _caFile,
          enabled: !_adding,
          onChanged: _changed,
        ),
        const SizedBox(height: Space.s16),
        Field(
          label: 'Token',
          hint: 'The token is written and never shown again',
          controller: _token,
          enabled: !_adding,
          onChanged: _changed,
          onSubmitted: (_) => _add(),
        ),
        if (failure != null) ...[
          const SizedBox(height: Space.s12),
          Text(failure, style: e.body.tint(e.err)),
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
/// removed, and one whose token the bridge refused is signed in to again. The
/// local daemon is neither — it is this machine's, and its profile is not the
/// Studio's to write.
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
  final _token = TextEditingController();
  String? _failure;
  bool _signingIn = false;

  @override
  void dispose() {
    _token.dispose();
    super.dispose();
  }

  /// A refused token is not a silence: a server profile that refused one is
  /// signed in to again, and one whose sign-in failed when it was added says
  /// why beside the field (decision 5).
  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final host = widget.host;
    final seen = host.lastContact;
    final server = host.kind.name != 'local';
    final loginFailure = widget.model.loginFailure(host.name);
    final failure = _failure;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        _Fact('Kind', HostsSection._kind(host)),
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
        if (server) ...[
          const SizedBox(height: Space.s16),
          Align(
            alignment: Alignment.centerLeft,
            child: Btn('Remove…', onTap: () => _remove(context)),
          ),
        ],
        if (server && (host.refused || loginFailure != null)) ...[
          const SizedBox(height: Space.s20),
          if (loginFailure != null) ...[
            Text(loginFailure, style: e.small.tint(e.err)),
            const SizedBox(height: Space.s12),
          ],
          Field(
            label: 'Token',
            hint: 'The token is written and never shown again',
            controller: _token,
            enabled: !_signingIn,
            onChanged: (_) {
              if (failure != null) setState(() => _failure = null);
            },
            onSubmitted: (_) => _signIn(),
          ),
          if (failure != null) ...[
            const SizedBox(height: Space.s12),
            Text(failure, style: e.small.tint(e.err)),
          ],
          const SizedBox(height: Space.s16),
          Align(
            alignment: Alignment.centerLeft,
            child: Btn.primary(
              _signingIn ? 'Signing in' : 'Sign in',
              onTap: _signingIn ? null : _signIn,
            ),
          ),
        ],
      ],
    );
  }

  Future<void> _signIn() async {
    final token = _token.text.trim();
    if (token.isEmpty) {
      setState(() => _failure = 'A token is needed');
      return;
    }
    setState(() {
      _signingIn = true;
      _failure = null;
    });
    try {
      await widget.model.signIn(widget.host.name, token);
      if (!mounted) return;
      _token.clear();
      setState(() => _signingIn = false);
    } catch (failure) {
      if (!mounted) return;
      setState(() {
        _signingIn = false;
        _failure = describeFailure(failure);
      });
    }
  }

  /// Removes the profile and its stored token, after the confirmation Access
  /// asks before a revoke: it names what is lost, and what keeps running.
  Future<void> _remove(BuildContext context) async {
    final host = widget.host;
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
              'The Studio stops showing it, and its profile and stored token '
              'are deleted. Its daemon keeps running.',
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
      setState(() => _failure = describeFailure(failure));
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
