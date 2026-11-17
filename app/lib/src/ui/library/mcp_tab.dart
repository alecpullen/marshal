import '../../model/clients_model.dart';
import '../../model/fleet_store.dart' show describeFailure;
import '../automations/confirm_action.dart';
import '../kit/kit.dart';
import '../sections/section_page.dart';
import 'library_place.dart' show LibraryTabs;
import 'placeholder.dart' show plannedMcpServers;

/// The MCP tab of Library (F7 decision 5): the registered clients, and each
/// one's confirmation rule and factory grants per project.
///
/// A client's token is shown once, when it is created, and never again: the
/// bridge does not send one after that, so nothing here can read one back.
class McpTab extends StatelessWidget {
  const McpTab({super.key, required this.model, this.tabs, this.state});

  final ClientsModel model;
  final LibraryTabs? tabs;
  final StateKind? state;

  @override
  Widget build(BuildContext context) {
    return SectionPage<MCPClient>(
      title: 'MCP',
      subtitle: 'What may drive Marshal from another editor',
      model: model,
      tabs: tabs?.items,
      tabIndex: tabs?.index ?? 0,
      onTab: tabs?.onChanged,
      state: state,
      readOnly: true,
      createLabel: 'New client',
      blank: ClientsModel.blank,
      detailTitle: (client) => client.id.isEmpty ? 'New client' : client.name,
      emptyMessage:
          'A client you register appears here, with the authority you give it '
          'per project.',
      footer: [
        Rows([plannedMcpServers()]),
      ],
      row: (client, onTap) => RowItem(
        name: client.name,
        description: _rule(client),
        value: (client.allowedRepos ?? const []).isEmpty
            ? 'Every repo'
            : '${client.allowedRepos!.length} repos',
        onTap: onTap,
      ),
      detail: (context, client) => client.id.isEmpty
          ? _NewClient(model: model)
          : _ClientDetail(model: model, client: client),
    );
  }

  /// Its confirmation rule, in the words §8 asks for.
  static String _rule(MCPClient client) => (client.autonomous ?? false)
      ? 'Acts without asking'
      : 'Asks before it acts';
}

/// The create form: a name, and then the token the bridge answers with, shown
/// once.
class _NewClient extends StatefulWidget {
  const _NewClient({required this.model});

  final ClientsModel model;

  @override
  State<_NewClient> createState() => _NewClientState();
}

class _NewClientState extends State<_NewClient> {
  final _name = TextEditingController();
  CreateClientResult? _created;
  var _busy = false;
  String? _error;

  @override
  void dispose() {
    _name.dispose();
    super.dispose();
  }

  Future<void> _create() async {
    final name = _name.text.trim();
    if (name.isEmpty) {
      setState(() => _error = 'Name the client first.');
      return;
    }
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final created = await widget.model.create(name);
      if (!mounted) return;
      setState(() {
        _busy = false;
        _created = created;
      });
    } catch (error) {
      if (!mounted) return;
      setState(() {
        _busy = false;
        _error = describeFailure(error);
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final created = _created;
    if (created != null) {
      return Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text(
            'The client exists. Its token is written below, and the bridge '
            'never shows it again: copy it into the client’s configuration '
            'now.',
            style: e.body,
          ),
          const SizedBox(height: Space.s12),
          Box(child: Text(created.token, style: e.mono)),
          const SizedBox(height: Space.s8),
          Text(
            'Its confirmation rule is “${created.autonomous ? 'acts without asking' : 'asks before it acts'}”.',
            style: e.small.tint(e.muted),
          ),
        ],
      );
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Field(label: 'Name', controller: _name, hint: 'editor'),
        const SizedBox(height: Space.s12),
        Align(
          alignment: Alignment.centerLeft,
          child: Btn.primary(
            _busy ? 'Creating' : 'Create',
            onTap: _busy ? null : _create,
          ),
        ),
        if (_error != null) ...[
          const SizedBox(height: Space.s12),
          Text(_error!, style: e.small.tint(e.err)),
        ],
      ],
    );
  }
}

/// One client's detail: its rule, its allowed repos, and its factory grants
/// per project — each one edited against the version it was read with.
class _ClientDetail extends StatefulWidget {
  const _ClientDetail({required this.model, required this.client});

  final ClientsModel model;
  final MCPClient client;

  @override
  State<_ClientDetail> createState() => _ClientDetailState();
}

class _ClientDetailState extends State<_ClientDetail> {
  String? _project;
  FactoryGrantState? _state;
  MCPFactoryProjectGrant? _grant;
  var _loading = false;
  var _saving = false;
  var _confirmingDelete = false;
  String? _note;
  String? _error;

  Future<void> _openGrant(String project) async {
    setState(() {
      _project = project;
      _loading = true;
      _state = null;
      _grant = null;
      _note = null;
      _error = null;
    });
    try {
      final state = await widget.model.grant(widget.client, project);
      if (!mounted) return;
      setState(() {
        _loading = false;
        _state = state;
        _grant = state.grant;
      });
    } catch (error) {
      if (!mounted) return;
      setState(() {
        _loading = false;
        _error = describeFailure(error);
      });
    }
  }

  Future<void> _saveGrant() async {
    final state = _state;
    final grant = _grant;
    if (state == null || grant == null) return;
    setState(() {
      _saving = true;
      _note = null;
      _error = null;
    });
    try {
      final saved = await widget.model.saveGrant(widget.client, state, grant);
      if (!mounted) return;
      setState(() {
        _saving = false;
        _state = saved;
        _grant = saved.grant;
        _note = 'Saved for ${saved.projectID}.';
      });
    } catch (error) {
      // A stale write keeps the edit and says so; the editor stays open.
      if (!mounted) return;
      setState(() {
        _saving = false;
        _error = describeFailure(error);
      });
    }
  }

  Future<void> _delete() async {
    setState(() => _error = null);
    try {
      await widget.model.remove(widget.client);
    } catch (error) {
      if (!mounted) return;
      setState(() => _error = describeFailure(error));
      return;
    }
    if (!mounted) return;
    Navigator.of(context).maybePop();
  }

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final client = widget.client;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        _Fact(label: 'Confirmation rule', value: McpTab._rule(client)),
        _Fact(
          label: 'Repositories',
          value: (client.allowedRepos ?? const []).isEmpty
              ? 'Every registered repository'
              : client.allowedRepos!.join(', '),
        ),
        _Fact(label: 'Registered', value: client.createdAt),
        if (_note != null) ...[
          const SizedBox(height: Space.s12),
          Text(_note!, style: e.small.tint(e.muted)),
        ],
        if (_error != null) ...[
          const SizedBox(height: Space.s12),
          Text(_error!, style: e.small.tint(e.err)),
        ],
        const SizedBox(height: Space.s20),
        if (_project == null) ..._grants(context) else ..._grantEditor(context),
        const SizedBox(height: Space.s20),
        if (_confirmingDelete)
          ConfirmAction(
            question: 'Delete ${client.name}?',
            loss:
                'Its token stops working and its factory grants are deleted. '
                'Nothing it started is stopped.',
            confirmLabel: 'Delete it',
            onConfirm: _delete,
            onCancel: () => setState(() => _confirmingDelete = false),
          )
        else
          Align(
            alignment: Alignment.centerLeft,
            child: Btn(
              'Delete this client',
              onTap: () => setState(() => _confirmingDelete = true),
            ),
          ),
        if (_project == null && _error == null) ...[
          const SizedBox(height: Space.s8),
          Text(
            'A token is never shown again after the client is created. Make a '
            'new client if it was lost.',
            style: e.small.tint(e.muted),
          ),
        ],
      ],
    );
  }

  /// The projects a grant can be written for, each one row.
  List<Widget> _grants(BuildContext context) {
    final e = Ember.of(context);
    if (widget.model.projects.isEmpty) {
      return [
        Text(
          'No project is registered, so there is nothing to grant yet.',
          style: e.small.tint(e.muted),
        ),
      ];
    }
    return [
      Label('Factory grants', count: widget.model.projects.length),
      const SizedBox(height: Space.s8),
      Rows([
        for (final project in widget.model.projects)
          RowItem(
            name: project,
            description: 'What this client may do in this project',
            value: 'Edit the grant',
            onTap: () => _openGrant(project),
          ),
      ]),
    ];
  }

  /// One project's grant, edited against the version it was read with.
  List<Widget> _grantEditor(BuildContext context) {
    final e = Ember.of(context);
    if (_loading) return const [StateMessage.loading(lines: 3)];
    final grant = _grant;
    if (grant == null) return const [];
    RowItem toggle(String name, String description, bool value, String key) =>
        RowItem(
          name: name,
          description: description,
          value: value ? 'On' : 'Off',
          onTap: () => setState(() {
            _grant = _toggled(grant, key);
            _note = null;
          }),
        );
    return [
      Align(
        alignment: Alignment.centerLeft,
        child: Btn(
          'Back to the client',
          onTap: () => setState(() {
            _project = null;
            _state = null;
            _grant = null;
          }),
        ),
      ),
      const SizedBox(height: Space.s16),
      Label('In $_project'),
      const SizedBox(height: Space.s8),
      Rows([
        toggle('Read', 'Read the project’s work', grant.read, 'read'),
        toggle(
          'Create a draft',
          'Draft a change for a person to review',
          grant.createDraft,
          'createDraft',
        ),
        toggle(
          'Edit a draft',
          'Change a draft before it is reviewed',
          grant.editDraft,
          'editDraft',
        ),
        toggle(
          'Answer a question',
          'Answer an agent’s question',
          grant.answerQuestion,
          'answerQuestion',
        ),
        toggle(
          'Approve and queue',
          'Approve a change and start the work',
          grant.approveAndQueue,
          'approveAndQueue',
        ),
        toggle(
          'Assess a plan change',
          'Judge a proposed plan change',
          grant.assessPlanChanges ?? false,
          'assessPlanChanges',
        ),
        toggle(
          'Approve a routine revision',
          'Approve a routine clarification',
          grant.approveRoutineRevision ?? false,
          'approveRoutineRevision',
        ),
      ]),
      const SizedBox(height: Space.s16),
      Text(
        'Approval authority: ${_authorityName(grant.approvalAuthority)}',
        style: e.body,
      ),
      const SizedBox(height: Space.s8),
      Wrap(
        spacing: Space.s8,
        runSpacing: Space.s8,
        children: [
          for (final authority in ApprovalAuthority.values)
            if (authority != ApprovalAuthority.unknown)
              Btn(
                _authorityName(authority),
                onTap: () => setState(() {
                  _grant = _withAuthority(grant, authority);
                  _note = null;
                }),
              ),
        ],
      ),
      const SizedBox(height: Space.s16),
      Align(
        alignment: Alignment.centerLeft,
        child: Btn.primary(
          _saving ? 'Saving' : 'Save the grant',
          onTap: _saving ? null : _saveGrant,
        ),
      ),
    ];
  }

  static MCPFactoryProjectGrant _toggled(
    MCPFactoryProjectGrant grant,
    String key,
  ) => MCPFactoryProjectGrant(
    projectID: grant.projectID,
    read: key == 'read' ? !grant.read : grant.read,
    createDraft: key == 'createDraft' ? !grant.createDraft : grant.createDraft,
    editDraft: key == 'editDraft' ? !grant.editDraft : grant.editDraft,
    answerQuestion: key == 'answerQuestion'
        ? !grant.answerQuestion
        : grant.answerQuestion,
    approveAndQueue: key == 'approveAndQueue'
        ? !grant.approveAndQueue
        : grant.approveAndQueue,
    approvalAuthority: grant.approvalAuthority,
    assessPlanChanges: key == 'assessPlanChanges'
        ? !(grant.assessPlanChanges ?? false)
        : grant.assessPlanChanges,
    approveRoutineRevision: key == 'approveRoutineRevision'
        ? !(grant.approveRoutineRevision ?? false)
        : grant.approveRoutineRevision,
  );

  static MCPFactoryProjectGrant _withAuthority(
    MCPFactoryProjectGrant grant,
    ApprovalAuthority authority,
  ) => MCPFactoryProjectGrant(
    projectID: grant.projectID,
    read: grant.read,
    createDraft: grant.createDraft,
    editDraft: grant.editDraft,
    answerQuestion: grant.answerQuestion,
    approveAndQueue: grant.approveAndQueue,
    approvalAuthority: authority,
    assessPlanChanges: grant.assessPlanChanges,
    approveRoutineRevision: grant.approveRoutineRevision,
  );

  static String _authorityName(ApprovalAuthority authority) =>
      switch (authority) {
        ApprovalAuthority.approvalAuthorityNone => 'No approval',
        ApprovalAuthority.approvalAuthorityUserDirected => 'User-directed',
        ApprovalAuthority.approvalAuthorityStanding => 'Standing',
        ApprovalAuthority.unknown => 'Unknown',
      };
}

/// One labelled fact of a read-only detail.
class _Fact extends StatelessWidget {
  const _Fact({required this.label, required this.value});

  final String label;
  final String value;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Padding(
      padding: const EdgeInsets.only(bottom: Space.s8),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(label, style: e.small.tint(e.muted)),
          const SizedBox(height: Space.s2),
          Text(value, style: e.body),
        ],
      ),
    );
  }
}
