import 'dart:async';

import '../../model/connections_model.dart';
import '../../model/fleet_store.dart' show describeFailure;
import '../automations/confirm_action.dart';
import '../kit/kit.dart';
import '../sections/section_page.dart';
import 'connection_forms.dart';
import 'webhook_setup.dart';

/// Settings' Connections section (F7 decision 7): the repositories Marshal
/// pushes to, and the model providers it runs on — and, from here, how each
/// is connected.
///
/// Every row leads with what is wrong when something is: a repository with no
/// credential cannot push, and says so in its row, not after it is opened.
/// A repository and a provider are added from the foot of the list, and a
/// repository's credential, a provider's key and their removal are in the
/// row's detail. Keys and tokens go one way: nothing here reads them back.
class ConnectionsSection extends StatelessWidget {
  const ConnectionsSection({super.key, required this.model, this.state});

  final ConnectionsModel model;

  /// The state the shell knows, when it knows more than the model does.
  final StateKind? state;

  @override
  Widget build(BuildContext context) {
    // Listening here, not only in the page, so the note above the rows is
    // drawn again when a fix removes the last thing it counted.
    return AnimatedBuilder(
      animation: model,
      builder: (context, _) => _page(context),
    );
  }

  Widget _page(BuildContext context) {
    final attention = model.items
        .where((connection) => connection.needsAttention)
        .length;
    return SectionPage<Connection>(
      title: 'Connections',
      subtitle: 'The repositories Marshal pushes to, and the model providers it runs on',
      model: model,
      state: state,
      readOnly: true,
      emptyMessage:
          'Nothing is connected yet. Connect a repository to let Marshal push '
          'to it, and a provider to give it a model to run.',
      emptyAction: model.canWrite
          ? StateAction(
              'Connect a repository',
              onTap: () =>
                  unawaited(showConnectRepositorySheet(context, model)),
            )
          : null,
      group: (connection) =>
          connection.isProvider ? 'Model providers' : 'Repositories',
      header: [if (attention > 0) _AttentionNote(attention)],
      row: (connection, onTap) => RowItem(
        name: connection.name,
        description: connection.description,
        value: connection.value,
        onTap: onTap,
      ),
      footer: [
        Rows([
          if (model.canWrite) ...[
            RowItem(
              name: 'Connect a repository',
              description: _repositoryHint,
              onTap: () =>
                  unawaited(showConnectRepositorySheet(context, model)),
            ),
            RowItem(
              name: 'Add a model provider',
              description: _providerHint,
              onTap: () => unawaited(showProviderSheet(context, model)),
            ),
          ] else ...const [
            RowItem.planned(
              name: 'Connect a repository',
              description: _repositoryHint,
            ),
            RowItem.planned(
              name: 'Add a model provider',
              description: _providerHint,
            ),
          ],
        ]),
      ],
      detail: (context, connection) => connection.isProvider
          ? _ProviderDetail(model: model, connection: connection)
          : _ForgeDetail(model: model, connection: connection),
    );
  }

  static const _repositoryHint =
      'A repository Marshal can read issues from and push branches to';
  static const _providerHint =
      'A local server or a hosted API that answers with models';
}

/// The line above the rows when something needs the person: how many
/// connections, and that each row says what.
class _AttentionNote extends StatelessWidget {
  const _AttentionNote(this.count);

  final int count;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Box(
      tone: BoxTone.attention,
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Padding(
            padding: const EdgeInsets.only(top: Space.s2),
            child: Icon(
              Glyphs.exclamationmark_circle,
              size: Sizes.iconRow,
              color: e.warn,
            ),
          ),
          const SizedBox(width: Space.s8),
          Expanded(
            child: Text(
              count == 1
                  ? '1 connection needs you. Open it to fix it.'
                  : '$count connections need you. Open one to fix it.',
              style: e.body,
            ),
          ),
        ],
      ),
    );
  }
}

/// The connection as the model holds it now, so a detail that changed
/// something draws what changed. Null once the connection is gone.
Connection? _current(ConnectionsModel model, Connection shown) {
  for (final connection in model.items) {
    if (model.keyOf(connection) == model.keyOf(shown)) return connection;
  }
  return null;
}

/// A repository's detail: what it is, its credential — which can be changed
/// or added here — its webhook, and removing it.
class _ForgeDetail extends StatefulWidget {
  const _ForgeDetail({required this.model, required this.connection});

  final ConnectionsModel model;
  final Connection connection;

  @override
  State<_ForgeDetail> createState() => _ForgeDetailState();
}

class _ForgeDetailState extends State<_ForgeDetail> {
  var _addingCredential = false;
  var _confirmingRemoval = false;
  var _busy = false;
  String? _failure;

  Future<void> _run(Future<void> Function() action) async {
    setState(() {
      _busy = true;
      _failure = null;
    });
    try {
      await action();
    } catch (error) {
      if (mounted) setState(() => _failure = describeFailure(error));
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _remove(Connection connection) async {
    await _run(() => widget.model.removeRepository(connection.id));
    if (!mounted || _failure != null) return;
    Navigator.of(context).maybePop();
  }

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: widget.model,
      builder: (context, _) {
        final connection = _current(widget.model, widget.connection);
        if (connection == null) return const SizedBox.shrink();
        final e = Ember.of(context);
        final failure = _failure;
        return Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            for (final (term, meaning) in connection.facts)
              if (term.isEmpty)
                Padding(
                  padding: const EdgeInsets.only(bottom: Space.s4),
                  child: Text(meaning, style: e.body),
                )
              else
                _Fact(term, meaning),
            const SizedBox(height: Space.s12),
            _credentialChoice(connection),
            if (_addingCredential) ...[
              const SizedBox(height: Space.s16),
              CredentialForm(
                model: widget.model,
                repoID: connection.id,
                onDone: () => setState(() => _addingCredential = false),
                onCancel: () => setState(() => _addingCredential = false),
              ),
            ],
            if (failure != null) ...[
              const SizedBox(height: Space.s8),
              Text(failure, style: e.small.tint(e.err)),
            ],
            const SizedBox(height: Space.s16),
            Text('Webhook', style: e.small.tint(e.muted)),
            const SizedBox(height: Space.s8),
            WebhookSetup(model: widget.model, repoID: connection.id),
            const SizedBox(height: Space.s20),
            if (_confirmingRemoval)
              ConfirmAction(
                question: 'Remove ${connection.name}?',
                loss:
                    'Marshal stops reading its issues and can no longer push '
                    'to it. The credential stays, and so do any projects '
                    'already discovered.',
                confirmLabel: 'Remove the repository',
                onConfirm: () => unawaited(_remove(connection)),
                onCancel: () => setState(() => _confirmingRemoval = false),
              )
            else
              Align(
                alignment: Alignment.centerLeft,
                child: Btn(
                  'Remove repository',
                  onTap: _busy
                      ? null
                      : () => setState(() => _confirmingRemoval = true),
                ),
              ),
          ],
        );
      },
    );
  }

  /// The credential in use, and the choices for changing it: another stored
  /// credential, none, or a new one made here.
  Widget _credentialChoice(Connection connection) {
    final current = connection.credentialID;
    final choices = widget.model.credentialChoices;
    return Align(
      alignment: Alignment.centerLeft,
      child: Menu(
        choices: [
          for (final choice in choices)
            MenuChoice(
              choice.id,
              '${choice.kind}${choice.isSet ? '' : ' · its secret is missing'}',
              onSelect: () => unawaited(
                _run(
                  () => widget.model.useCredential(connection.id, choice.id),
                ),
              ),
            ),
          MenuChoice(
            'A new credential',
            'Paste a token, or name an environment variable or an SSH key',
            onSelect: () => setState(() => _addingCredential = true),
          ),
          if (current != null)
            MenuChoice(
              'No credential',
              'Marshal can no longer push to this repository',
              onSelect: () => unawaited(
                _run(() => widget.model.useCredential(connection.id, null)),
              ),
            ),
        ],
        anchor: (context, toggle) => Btn(
          _busy
              ? 'Saving'
              : current == null
              ? 'Choose a credential'
              : 'Change credential',
          onTap: _busy ? null : toggle,
        ),
      ),
    );
  }
}

/// A provider's detail: what it is, a key to set or replace, what it answers
/// when probed, and removing it.
class _ProviderDetail extends StatefulWidget {
  const _ProviderDetail({required this.model, required this.connection});

  final ConnectionsModel model;
  final Connection connection;

  @override
  State<_ProviderDetail> createState() => _ProviderDetailState();
}

class _ProviderDetailState extends State<_ProviderDetail> {
  var _probing = false;
  var _settingKey = false;
  var _confirmingRemoval = false;
  String? _error;

  Future<void> _probe() async {
    setState(() {
      _probing = true;
      _error = null;
    });
    try {
      await widget.model.probe(widget.connection);
    } catch (error) {
      if (mounted) setState(() => _error = describeFailure(error));
    } finally {
      if (mounted) setState(() => _probing = false);
    }
  }

  Future<void> _remove() async {
    setState(() => _error = null);
    try {
      await widget.model.removeProvider(widget.connection.id);
    } catch (error) {
      if (mounted) setState(() => _error = describeFailure(error));
      return;
    }
    if (mounted) Navigator.of(context).maybePop();
  }

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: widget.model,
      builder: (context, _) {
        final connection = _current(widget.model, widget.connection);
        if (connection == null) return const SizedBox.shrink();
        final e = Ember.of(context);
        final probe = widget.model.probeOf(connection.id);
        final error = _error;
        return Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            for (final (term, meaning) in connection.facts)
              _Fact(term, meaning),
            const SizedBox(height: Space.s4),
            if (probe != null) _ProbeAnswer(probe),
            if (error != null) ...[
              const SizedBox(height: Space.s8),
              Text(error, style: e.body.tint(e.err)),
            ],
            const SizedBox(height: Space.s12),
            Wrap(
              spacing: Space.s8,
              runSpacing: Space.s8,
              children: [
                Btn(
                  _probing ? 'Probing' : 'Probe',
                  onTap: _probing ? null : _probe,
                ),
                Btn(
                  'Set key',
                  onTap: _settingKey
                      ? null
                      : () => setState(() => _settingKey = true),
                ),
              ],
            ),
            if (_settingKey) ...[
              const SizedBox(height: Space.s16),
              ProviderKeyForm(
                model: widget.model,
                name: connection.id,
                onDone: () => setState(() => _settingKey = false),
                onCancel: () => setState(() => _settingKey = false),
              ),
            ],
            const SizedBox(height: Space.s20),
            if (_confirmingRemoval)
              ConfirmAction(
                question: 'Remove ${connection.name}?',
                loss:
                    'Any role or preset that runs on it stops working until '
                    'it is pointed at another provider. This is your global '
                    'config, so every project loses it.',
                confirmLabel: 'Remove the provider',
                onConfirm: () => unawaited(_remove()),
                onCancel: () => setState(() => _confirmingRemoval = false),
              )
            else
              Align(
                alignment: Alignment.centerLeft,
                child: Btn(
                  'Remove provider',
                  onTap: () => setState(() => _confirmingRemoval = true),
                ),
              ),
          ],
        );
      },
    );
  }
}

/// What a probe answered: the models it found, or what went wrong.
class _ProbeAnswer extends StatelessWidget {
  const _ProbeAnswer(this.answer);

  final ProbeAnswer answer;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    if (answer.failed) {
      return Text(
        'The probe failed: ${answer.error}',
        style: e.body.tint(e.err),
      );
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(
          '${answer.count} ${answer.count == 1 ? 'model' : 'models'}',
          style: e.small.tint(e.muted),
        ),
        const SizedBox(height: Space.s4),
        Text(
          [
            for (final (id, window) in answer.models)
              '$id${window == null ? '' : ' · $window tokens'}',
          ].join('\n'),
          style: e.mono,
        ),
      ],
    );
  }
}

/// One named fact.
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
