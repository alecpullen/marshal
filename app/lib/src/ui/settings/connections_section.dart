import '../../model/connections_model.dart';
import '../../model/fleet_store.dart' show describeFailure;
import '../kit/kit.dart';
import '../sections/section_page.dart';

/// Settings' Connections section (F7 decision 7): the forges Marshal pushes
/// to, with the credential's kind and what it permits, and the model
/// providers with their health and a probe.
class ConnectionsSection extends StatelessWidget {
  const ConnectionsSection({super.key, required this.model, this.state});

  final ConnectionsModel model;

  /// The state the shell knows, when it knows more than the model does.
  final StateKind? state;

  @override
  Widget build(BuildContext context) {
    return SectionPage<Connection>(
      title: 'Connections',
      subtitle:
          'The forges Marshal reaches, and the model providers it runs on',
      model: model,
      state: state,
      readOnly: true,
      emptyMessage: 'No repository or provider is connected yet.',
      row: (connection, onTap) => RowItem(
        name: connection.name,
        description: connection.description,
        value: connection.value,
        onTap: onTap,
      ),
      detail: (context, connection) =>
          _ConnectionDetail(model: model, connection: connection),
    );
  }
}

/// One connection's detail: what it is, what its credential permits, and —
/// for a provider — what it answers when probed.
class _ConnectionDetail extends StatefulWidget {
  const _ConnectionDetail({required this.model, required this.connection});

  final ConnectionsModel model;
  final Connection connection;

  @override
  State<_ConnectionDetail> createState() => _ConnectionDetailState();
}

class _ConnectionDetailState extends State<_ConnectionDetail> {
  var _probing = false;
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

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final connection = widget.connection;
    final probe = widget.model.probeOf(connection.id);
    final error = _error;
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
        if (connection.isProvider) ...[
          const SizedBox(height: Space.s16),
          if (probe != null) _ProbeAnswer(probe),
          if (error != null) ...[
            const SizedBox(height: Space.s8),
            Text(error, style: e.body.tint(e.err)),
          ],
          const SizedBox(height: Space.s12),
          Align(
            alignment: Alignment.centerLeft,
            child: Btn(
              _probing ? 'Probing' : 'Probe',
              onTap: _probing ? null : _probe,
            ),
          ),
        ],
      ],
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
