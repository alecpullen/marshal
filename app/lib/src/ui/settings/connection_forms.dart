import 'dart:async';

import '../../model/connect_repo_model.dart' show ConnectRepoModel;
import '../../model/connections_model.dart';
import '../../model/fleet_store.dart' show describeFailure;
import '../kit/kit.dart';

/// The sheet the Connections footer opens to connect a repository: its URL,
/// the forge, and the credential it is reached with.
///
/// A repository with no credential is still connected — it can be read for
/// issues — and its row then says it cannot push, so the next step is in
/// front of the person rather than in a document.
Future<void> showConnectRepositorySheet(
  BuildContext context,
  ConnectionsModel model,
) => Sheet.show<void>(
  context,
  Sheet(
    title: 'Connect a repository',
    child: ConnectRepositoryForm(model: model),
  ),
);

/// The sheet the Connections footer opens to add a model provider.
Future<void> showProviderSheet(BuildContext context, ConnectionsModel model) =>
    Sheet.show<void>(
      context,
      Sheet(
        title: 'Add a model provider',
        child: ProviderForm(model: model),
      ),
    );

/// One form's submit, shared by all four: it shows saving, keeps what was
/// typed when the bridge refuses, and says why in the bridge's own words.
mixin _Submitting<T extends StatefulWidget> on State<T> {
  var busy = false;
  String? failure;

  /// Runs [action]; returns whether it succeeded.
  Future<bool> submit(Future<void> Function() action) async {
    setState(() {
      busy = true;
      failure = null;
    });
    try {
      await action();
      return true;
    } catch (error) {
      if (mounted) setState(() => failure = describeFailure(error));
      return false;
    } finally {
      if (mounted) setState(() => busy = false);
    }
  }

  /// Typing clears the last refusal: it was about what was there before.
  void typed(String _) {
    if (failure != null) setState(() => failure = null);
  }
}

/// A field's emptiness means "not stated", not the empty string.
String? _optional(String text) {
  final trimmed = text.trim();
  return trimmed.isEmpty ? null : trimmed;
}

/// A choice from a short list, drawn as a button that opens a [Menu].
class _Choice extends StatelessWidget {
  const _Choice({
    required this.label,
    required this.value,
    required this.choices,
    required this.enabled,
  });

  final String label;
  final String value;
  final List<MenuChoice> choices;
  final bool enabled;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(label, style: e.small.tint(e.muted)),
        const SizedBox(height: Space.s6),
        Menu(
          choices: choices,
          anchor: (context, toggle) =>
              Btn(value, onTap: enabled ? toggle : null),
        ),
      ],
    );
  }
}

class _Refusal extends StatelessWidget {
  const _Refusal(this.message);

  final String message;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Padding(
      padding: const EdgeInsets.only(top: Space.s12),
      child: Text(message, style: e.small.tint(e.err)),
    );
  }
}

/// Connecting a repository.
class ConnectRepositoryForm extends StatefulWidget {
  const ConnectRepositoryForm({super.key, required this.model});

  final ConnectionsModel model;

  @override
  State<ConnectRepositoryForm> createState() => _ConnectRepositoryFormState();
}

class _ConnectRepositoryFormState extends State<ConnectRepositoryForm>
    with _Submitting {
  final _url = TextEditingController();
  final _id = TextEditingController();
  final _branch = TextEditingController();
  var _forge = 'github';
  String? _credential;

  /// The id follows the URL until someone edits it themselves.
  var _idEdited = false;

  @override
  void dispose() {
    _url.dispose();
    _id.dispose();
    _branch.dispose();
    super.dispose();
  }

  Future<void> _connect() async {
    final url = _url.text.trim();
    final id = _id.text.trim();
    final done = await submit(
      () => widget.model.connectRepository(
        url: url,
        id: id.isEmpty ? ConnectRepoModel.idFor(url) : id,
        forge: _forge,
        credentialID: _credential,
        branch: _optional(_branch.text),
      ),
    );
    if (done && mounted) Navigator.of(context).maybePop();
  }

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final choices = widget.model.credentialChoices;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      mainAxisSize: MainAxisSize.min,
      children: [
        Field(
          label: 'Repository URL',
          hint: 'https://github.com/acme/marshal.git',
          controller: _url,
          enabled: !busy,
          onChanged: (url) {
            if (!_idEdited) _id.text = ConnectRepoModel.idFor(url);
            typed(url);
            setState(() {});
          },
        ),
        const SizedBox(height: Space.s16),
        Field(
          label: 'Name',
          hint: 'acme-marshal',
          controller: _id,
          enabled: !busy,
          onChanged: (text) {
            _idEdited = true;
            typed(text);
          },
        ),
        const SizedBox(height: Space.s16),
        _Choice(
          label: 'Forge',
          value: _forge == 'gitea' ? 'Gitea' : 'GitHub',
          enabled: !busy,
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
        ),
        const SizedBox(height: Space.s16),
        _Choice(
          label: 'Credential',
          value: _credential ?? 'None yet',
          enabled: !busy,
          choices: [
            MenuChoice(
              'None yet',
              'It can be read for issues; add one to let Marshal push',
              onSelect: () => setState(() => _credential = null),
            ),
            for (final choice in choices)
              MenuChoice(
                choice.id,
                '${choice.kind}${choice.isSet ? '' : ' · its secret is missing'}',
                onSelect: () => setState(() => _credential = choice.id),
              ),
          ],
        ),
        const SizedBox(height: Space.s16),
        Field(
          label: 'Branch',
          hint: 'optional: the branch intake reads',
          controller: _branch,
          enabled: !busy,
          onChanged: typed,
          onSubmitted: (_) => unawaited(_connect()),
        ),
        const SizedBox(height: Space.s12),
        Text(
          'A repository is connected for issue intake. A project appears when '
          'the daemon discovers one, not when a repository is connected.',
          style: e.small.tint(e.muted),
        ),
        if (failure case final message?) _Refusal(message),
        const SizedBox(height: Space.s20),
        Align(
          alignment: Alignment.centerLeft,
          child: Btn.primary(
            busy ? 'Connecting' : 'Connect the repository',
            onTap: busy || _url.text.trim().isEmpty
                ? null
                : () => unawaited(_connect()),
          ),
        ),
      ],
    );
  }
}

/// A credential for one repository, made in place in its detail: a token
/// pasted here, an environment variable the daemon reads, or an SSH key file
/// on the daemon's machine.
class CredentialForm extends StatefulWidget {
  const CredentialForm({
    super.key,
    required this.model,
    required this.repoID,
    required this.onDone,
    required this.onCancel,
  });

  final ConnectionsModel model;

  /// The repository the credential is for, which is pointed at it once it is
  /// stored.
  final String repoID;

  final VoidCallback onDone;
  final VoidCallback onCancel;

  @override
  State<CredentialForm> createState() => _CredentialFormState();
}

class _CredentialFormState extends State<CredentialForm> with _Submitting {
  late final _id = TextEditingController(text: '${widget.repoID}-token');
  final _value = TextEditingController();
  var _source = CredentialSource.pastedToken;

  @override
  void dispose() {
    _id.dispose();
    _value.dispose();
    super.dispose();
  }

  Future<void> _save() async {
    final done = await submit(
      () => widget.model.addCredential(
        id: _id.text.trim(),
        source: _source,
        value: _value.text.trim(),
        repoID: widget.repoID,
      ),
    );
    if (done) widget.onDone();
  }

  void _pick(CredentialSource source) {
    _value.clear();
    setState(() {
      _source = source;
      failure = null;
    });
  }

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final (label, hint, note) = switch (_source) {
      CredentialSource.pastedToken => (
        'Token',
        'A personal access token with repo scope',
        'Stored in the daemon’s secrets and never shown again. It can push, '
            'open pull requests and read checks.',
      ),
      CredentialSource.environmentVariable => (
        'Variable name',
        'GITHUB_TOKEN',
        'The daemon reads this variable from its own environment. It must '
            'be set where the daemon runs.',
      ),
      CredentialSource.sshKey => (
        'Key file',
        '/home/you/.ssh/id_ed25519',
        'Pushes only: an SSH key cannot open pull requests or read checks.',
      ),
    };
    return Box(
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text('New credential', style: e.bodyStrong),
          const SizedBox(height: Space.s12),
          _Choice(
            label: 'It holds',
            enabled: !busy,
            value: switch (_source) {
              CredentialSource.pastedToken => 'A pasted token',
              CredentialSource.environmentVariable => 'An environment variable',
              CredentialSource.sshKey => 'An SSH key file',
            },
            choices: [
              MenuChoice(
                'A pasted token',
                'The easiest: nothing to set on the daemon’s machine',
                onSelect: () => _pick(CredentialSource.pastedToken),
              ),
              MenuChoice(
                'An environment variable',
                'A token the daemon already has in its environment',
                onSelect: () => _pick(CredentialSource.environmentVariable),
              ),
              MenuChoice(
                'An SSH key file',
                'A private key on the daemon’s machine',
                onSelect: () => _pick(CredentialSource.sshKey),
              ),
            ],
          ),
          const SizedBox(height: Space.s12),
          Field(
            label: 'Name',
            controller: _id,
            enabled: !busy,
            onChanged: typed,
          ),
          const SizedBox(height: Space.s12),
          Field(
            key: ValueKey(_source),
            label: label,
            hint: hint,
            controller: _value,
            enabled: !busy,
            mono: _source != CredentialSource.pastedToken,
            onChanged: (text) {
              typed(text);
              setState(() {});
            },
            onSubmitted: (_) => unawaited(_save()),
          ),
          const SizedBox(height: Space.s8),
          Text(note, style: e.small.tint(e.muted)),
          if (failure case final message?) _Refusal(message),
          const SizedBox(height: Space.s16),
          Wrap(
            spacing: Space.s8,
            runSpacing: Space.s8,
            children: [
              Btn.primary(
                busy ? 'Saving' : 'Save credential',
                onTap: busy || _value.text.trim().isEmpty
                    ? null
                    : () => unawaited(_save()),
              ),
              Btn('Cancel', onTap: busy ? null : widget.onCancel),
            ],
          ),
        ],
      ),
    );
  }
}

/// Adding a provider: a name, the protocol it speaks, where it answers, and
/// optionally its key.
class ProviderForm extends StatefulWidget {
  const ProviderForm({super.key, required this.model});

  final ConnectionsModel model;

  @override
  State<ProviderForm> createState() => _ProviderFormState();
}

class _ProviderFormState extends State<ProviderForm> with _Submitting {
  final _name = TextEditingController();
  final _url = TextEditingController(text: 'http://127.0.0.1:1234/v1');
  final _key = TextEditingController();
  var _type = providerTypes.first.$1;

  /// The URL follows the type until someone edits it themselves.
  var _urlEdited = false;

  @override
  void dispose() {
    _name.dispose();
    _url.dispose();
    _key.dispose();
    super.dispose();
  }

  static String _defaultUrl(String type) => switch (type) {
    'anthropic' => 'https://api.anthropic.com',
    'ollama' => 'http://127.0.0.1:11434',
    _ => 'http://127.0.0.1:1234/v1',
  };

  Future<void> _save() async {
    final done = await submit(
      () => widget.model.saveProvider(
        name: _name.text.trim(),
        type: _type,
        baseURL: _url.text.trim(),
        key: _optional(_key.text),
      ),
    );
    if (done && mounted) Navigator.of(context).maybePop();
  }

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final typeName = providerTypes.firstWhere((one) => one.$1 == _type).$2;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      mainAxisSize: MainAxisSize.min,
      children: [
        Field(
          label: 'Name',
          hint: 'lmstudio',
          controller: _name,
          enabled: !busy,
          onChanged: (text) {
            typed(text);
            setState(() {});
          },
        ),
        const SizedBox(height: Space.s16),
        _Choice(
          label: 'Protocol',
          value: typeName,
          enabled: !busy,
          choices: [
            for (final (type, name, about) in providerTypes)
              MenuChoice(
                name,
                about,
                onSelect: () => setState(() {
                  _type = type;
                  if (!_urlEdited) _url.text = _defaultUrl(type);
                }),
              ),
          ],
        ),
        const SizedBox(height: Space.s16),
        Field(
          label: 'Base URL',
          controller: _url,
          mono: true,
          enabled: !busy,
          onChanged: (text) {
            _urlEdited = true;
            typed(text);
            setState(() {});
          },
        ),
        const SizedBox(height: Space.s16),
        Field(
          label: 'API key',
          hint: 'optional: a local server needs none',
          controller: _key,
          enabled: !busy,
          onChanged: typed,
          onSubmitted: (_) => unawaited(_save()),
        ),
        const SizedBox(height: Space.s12),
        Text(
          'Saved to your global config, so every project can use it. A key is '
          'written and never read back. Probe the provider afterwards to '
          'check it answers.',
          style: e.small.tint(e.muted),
        ),
        if (failure case final message?) _Refusal(message),
        const SizedBox(height: Space.s20),
        Align(
          alignment: Alignment.centerLeft,
          child: Btn.primary(
            busy ? 'Saving' : 'Add the provider',
            onTap: busy || _name.text.trim().isEmpty || _url.text.trim().isEmpty
                ? null
                : () => unawaited(_save()),
          ),
        ),
      ],
    );
  }
}

/// A provider's key, set or replaced in place in its detail.
class ProviderKeyForm extends StatefulWidget {
  const ProviderKeyForm({
    super.key,
    required this.model,
    required this.name,
    required this.onDone,
    required this.onCancel,
  });

  final ConnectionsModel model;
  final String name;
  final VoidCallback onDone;
  final VoidCallback onCancel;

  @override
  State<ProviderKeyForm> createState() => _ProviderKeyFormState();
}

class _ProviderKeyFormState extends State<ProviderKeyForm> with _Submitting {
  final _key = TextEditingController();

  @override
  void dispose() {
    _key.dispose();
    super.dispose();
  }

  Future<void> _save() async {
    final done = await submit(
      () => widget.model.setProviderKey(widget.name, _key.text.trim()),
    );
    if (done) widget.onDone();
  }

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Box(
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Field(
            label: 'New key',
            hint: 'Paste the key; it is never shown again',
            controller: _key,
            enabled: !busy,
            onChanged: (text) {
              typed(text);
              setState(() {});
            },
            onSubmitted: (_) => unawaited(_save()),
          ),
          const SizedBox(height: Space.s8),
          Text(
            'Replaces the key this provider has now.',
            style: e.small.tint(e.muted),
          ),
          if (failure case final message?) _Refusal(message),
          const SizedBox(height: Space.s12),
          Wrap(
            spacing: Space.s8,
            runSpacing: Space.s8,
            children: [
              Btn.primary(
                busy ? 'Saving' : 'Save key',
                onTap: busy || _key.text.trim().isEmpty
                    ? null
                    : () => unawaited(_save()),
              ),
              Btn('Cancel', onTap: busy ? null : widget.onCancel),
            ],
          ),
        ],
      ),
    );
  }
}
