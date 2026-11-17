import '../../model/access_model.dart';
import '../kit/kit.dart';
import '../sections/section_page.dart';

/// Settings' Access section (F7 decision 7): the Studio's token, where the MCP
/// clients are, and the status links — listed, made and revoked.
///
/// Access does not list the MCP clients a second time: its row says where they
/// are, and the plan's decision names Library's MCP tab as their one home.
class AccessSection extends StatelessWidget {
  const AccessSection({super.key, required this.model, this.state});

  final AccessModel model;

  /// The state the shell knows, when it knows more than the model does.
  final StateKind? state;

  @override
  Widget build(BuildContext context) {
    return SectionPage<AccessRow>(
      title: 'Access',
      subtitle: 'The Studio’s token, and the links that let others watch',
      model: model,
      state: state,
      createLabel: 'New status link',
      blank: AccessModel.blank,
      detailTitle: (row) =>
          row.kind == AccessRowKind.newLink ? 'New status link' : row.name,
      emptyMessage: 'No status link exists yet.',
      row: (row, onTap) => RowItem(
        name: row.kind == AccessRowKind.link ? row.name : row.name,
        description: row.description,
        value: row.value,
        onTap: row.kind == AccessRowKind.mcp ? null : onTap,
        mono: row.kind == AccessRowKind.link,
      ),
      detail: (context, row) => _AccessDetail(model: model, row: row),
    );
  }
}

/// One row's detail: what the token is, where the clients are, one link's
/// record with Revoke, or the fields a new link needs.
class _AccessDetail extends StatelessWidget {
  const _AccessDetail({required this.model, required this.row});

  final AccessModel model;
  final AccessRow row;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    switch (row.kind) {
      case AccessRowKind.token:
        return Text(
          model.tokenHeld
              ? 'This Studio holds a bearer token for its host. It is kept by '
                    'the client that opens the socket, and no screen shows it.'
              : 'This host needs no token, so the Studio sends none.',
          style: e.body.tint(e.muted),
        );
      case AccessRowKind.mcp:
        return Text(
          'The MCP clients, their tokens and their factory grants are in '
          'Library’s MCP tab. Access points there rather than keeping a '
          'second list of the same clients.',
          style: e.body.tint(e.muted),
        );
      case AccessRowKind.link:
        return _LinkDetail(model: model, row: row);
      case AccessRowKind.newLink:
        return _NewLinkDetail(model: model, row: row);
    }
  }
}

/// One link: what it is for, and the revoke that stops it working.
class _LinkDetail extends StatelessWidget {
  const _LinkDetail({required this.model, required this.row});

  final AccessModel model;
  final AccessRow row;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final link = row.link;
    final made = model.made;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        _Fact('Agent', row.agentID),
        if (link != null) ...[
          _Fact('Created', link.createdAt),
          _Fact('Expires', link.expiresAt),
          _Fact('State', row.value),
        ],
        if (made != null && link != null && made.id == link.id) ...[
          const SizedBox(height: Space.s12),
          Box(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  'This link’s URL, shown once',
                  style: e.small.tint(e.muted),
                ),
                const SizedBox(height: Space.s4),
                Text(made.url, style: e.mono),
              ],
            ),
          ),
        ],
        const SizedBox(height: Space.s16),
        Align(
          alignment: Alignment.centerLeft,
          child: Btn(
            'Revoke…',
            onTap: link == null || row.value.startsWith('Revoked')
                ? null
                : () => _revoke(context),
          ),
        ),
      ],
    );
  }

  Future<void> _revoke(BuildContext context) async {
    final confirmed = await Sheet.show<bool>(
      context,
      Sheet(
        title: 'Revoke this link?',
        actions: [Btn('Cancel', onTap: () => Navigator.maybePop(context))],
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              'The link for ${row.agentID} stops working at once. Anyone '
              'holding its URL sees nothing more; a new link is a new URL.',
            ),
            const SizedBox(height: Space.s16),
            Align(
              alignment: Alignment.centerLeft,
              child: Btn.primary(
                'Revoke',
                onTap: () => Navigator.maybePop(context, true),
              ),
            ),
          ],
        ),
      ),
    );
    if (confirmed != true) return;
    await model.revoke(row);
  }
}

/// A link being made: its agent and how long it lasts.
class _NewLinkDetail extends StatefulWidget {
  const _NewLinkDetail({required this.model, required this.row});

  final AccessModel model;
  final AccessRow row;

  @override
  State<_NewLinkDetail> createState() => _NewLinkDetailState();
}

class _NewLinkDetailState extends State<_NewLinkDetail> {
  final _agent = TextEditingController();
  final _hours = TextEditingController(text: '24');
  String? _error;

  @override
  void initState() {
    super.initState();
    _agent.text = widget.row.agentID;
  }

  @override
  void dispose() {
    _agent.dispose();
    _hours.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Field(
          label: 'Agent',
          hint: 'a-1',
          controller: _agent,
          onChanged: (text) {
            widget.model.change(widget.row.withAgent(text));
            setState(() => _error = null);
          },
        ),
        const SizedBox(height: Space.s16),
        Field(
          label: 'Hours it lasts',
          hint: '24',
          controller: _hours,
          onChanged: (text) {
            final hours = int.tryParse(text.trim());
            if (hours == null) {
              setState(() => _error = 'Hours is a whole number');
              return;
            }
            widget.model.change(widget.row.withHours(hours));
            setState(() => _error = null);
          },
        ),
        if (_error != null) ...[
          const SizedBox(height: Space.s8),
          Text(
            _error!,
            style: Ember.of(context).small.tint(Ember.of(context).err),
          ),
        ],
      ],
    );
  }
}

/// One named fact of a link.
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
