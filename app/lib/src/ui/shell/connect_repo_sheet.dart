import 'dart:async';

import '../../model/connect_repo_model.dart';
import '../kit/kit.dart';

/// The sheet the sidebar's plus opens (F9 B1): register a repository for issue
/// intake, then offer the discovery that makes its issues appear.
///
/// It says what the bridge does rather than what a project screen would: a
/// repository is registered, and a project appears when the daemon discovers
/// one (f9-parity decision 1).
Future<void> showConnectRepoSheet(
  BuildContext context,
  ConnectRepoModel model,
) => Sheet.show<void>(
  context,
  Sheet(
    title: 'Connect a repository',
    child: _ConnectRepoSheet(model: model),
  ),
);

class _ConnectRepoSheet extends StatefulWidget {
  const _ConnectRepoSheet({required this.model});

  final ConnectRepoModel model;

  @override
  State<_ConnectRepoSheet> createState() => _ConnectRepoSheetState();
}

class _ConnectRepoSheetState extends State<_ConnectRepoSheet> {
  final _url = TextEditingController();
  final _id = TextEditingController();
  final _credRef = TextEditingController();
  final _branch = TextEditingController();

  String _forge = 'github';

  /// The id follows the URL until someone edits it themselves; after that it
  /// is theirs and the URL leaves it alone.
  bool _idEdited = false;

  @override
  void initState() {
    super.initState();
    widget.model.addListener(_changed);
  }

  @override
  void dispose() {
    widget.model.removeListener(_changed);
    for (final controller in [_url, _id, _credRef, _branch]) {
      controller.dispose();
    }
    super.dispose();
  }

  void _changed() {
    if (mounted) setState(() {});
  }

  /// What the person typed, without the whitespace around it.
  String get _urlValue => _url.text.trim();

  Future<void> _connect() async {
    final id = _id.text.trim();
    await widget.model.register(
      url: _urlValue,
      id: id.isEmpty ? ConnectRepoModel.idFor(_urlValue) : id,
      forge: _forge,
      credRef: _optional(_credRef.text),
      branch: _optional(_branch.text),
    );
  }

  /// A field whose emptiness means "not stated", not "the empty string".
  static String? _optional(String text) {
    final trimmed = text.trim();
    return trimmed.isEmpty ? null : trimmed;
  }

  @override
  Widget build(BuildContext context) {
    final model = widget.model;
    final registered = model.registered;
    final failure = model.failure;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      mainAxisSize: MainAxisSize.min,
      children: [
        if (registered != null)
          _registered(context, registered, failure)
        else
          _form(context, failure),
      ],
    );
  }

  Widget _form(BuildContext context, String? failure) {
    final e = Ember.of(context);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      mainAxisSize: MainAxisSize.min,
      children: [
        Field(
          label: 'Repository URL',
          hint: 'https://github.com/acme/marshal.git',
          controller: _url,
          onChanged: (url) {
            if (!_idEdited) _id.text = ConnectRepoModel.idFor(url);
            setState(() {});
          },
        ),
        const SizedBox(height: Space.s16),
        Field(
          label: 'Repository id',
          hint: 'acme-marshal',
          controller: _id,
          onChanged: (_) {
            _idEdited = true;
            setState(() {});
          },
        ),
        const SizedBox(height: Space.s16),
        Text('Forge', style: e.small.tint(e.muted)),
        const SizedBox(height: Space.s6),
        Align(
          alignment: Alignment.centerLeft,
          child: Menu(
            choices: [
              MenuChoice(
                'GitHub',
                'github.com, or an enterprise GitHub',
                onSelect: () => setState(() => _forge = 'github'),
              ),
              MenuChoice(
                'Gitea',
                'A self-hosted Gitea',
                onSelect: () => setState(() => _forge = 'gitea'),
              ),
            ],
            anchor: (context, toggle) =>
                Btn(_forge == 'gitea' ? 'Gitea' : 'GitHub', onTap: toggle),
          ),
        ),
        const SizedBox(height: Space.s16),
        Field(
          label: 'Credential reference',
          hint: 'optional: the id of a stored forge credential',
          controller: _credRef,
          onChanged: (_) => setState(() {}),
        ),
        const SizedBox(height: Space.s16),
        Field(
          label: 'Branch',
          hint: 'optional: the branch intake reads',
          controller: _branch,
          onChanged: (_) => setState(() {}),
        ),
        const SizedBox(height: Space.s16),
        Text(
          'A repository is registered for issue intake. A project appears '
          'when the daemon discovers one — not when a repository is '
          'registered.',
          style: e.small.tint(e.muted),
        ),
        if (failure != null) ...[
          const SizedBox(height: Space.s16),
          StateMessage.error(
            message: failure,
            retry: StateAction('Try again', onTap: () => unawaited(_connect())),
          ),
        ],
        const SizedBox(height: Space.s20),
        Align(
          alignment: Alignment.centerLeft,
          child: Btn.primary(
            'Connect the repository',
            onTap: widget.model.busy || _urlValue.isEmpty
                ? null
                : () => unawaited(_connect()),
          ),
        ),
      ],
    );
  }

  /// What the sheet shows once the bridge has the repository: the URL it kept,
  /// and the discovery that makes its issues appear.
  Widget _registered(BuildContext context, Repo repo, String? failure) {
    final e = Ember.of(context);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      mainAxisSize: MainAxisSize.min,
      children: [
        Text('The bridge registered', style: e.small.tint(e.muted)),
        const SizedBox(height: Space.s4),
        Text(repo.url, style: e.mono),
        const SizedBox(height: Space.s12),
        Text(
          'Discover its issues to make them appear for intake.',
          style: e.small.tint(e.muted),
        ),
        if (failure != null) ...[
          const SizedBox(height: Space.s16),
          StateMessage.error(
            message: failure,
            retry: StateAction(
              'Try again',
              onTap: () => unawaited(widget.model.discover()),
            ),
          ),
        ],
        const SizedBox(height: Space.s20),
        Align(
          alignment: Alignment.centerLeft,
          child: Btn(
            'Discover its issues',
            onTap: () => unawaited(widget.model.discover()),
          ),
        ),
      ],
    );
  }
}
