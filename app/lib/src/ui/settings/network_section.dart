import '../../model/network_model.dart';
import '../kit/kit.dart';
import '../sections/section_page.dart';

/// Settings' Network section (F7 decision 7): the egress hosts with the rule
/// that covers each, their request counts and when they were last seen, shown
/// for one environment and agent or for all of them.
class NetworkSection extends StatelessWidget {
  const NetworkSection({super.key, required this.model, this.state});

  final NetworkModel model;

  /// The state the shell knows, when it knows more than the model does.
  final StateKind? state;

  @override
  Widget build(BuildContext context) {
    return SectionPage<NetworkRow>(
      title: 'Network',
      subtitle: 'What the agents reached, and what the proxy let through',
      model: model,
      state: state,
      readOnly: true,
      emptyMessage: 'Nothing has reached the network yet.',
      row: (row, onTap) => RowItem(
        name: row.name,
        description: row.description,
        value: row.value,
        onTap: onTap,
      ),
      detail: (context, row) => row.kind == NetworkRowKind.filter
          ? _FilterDetail(model: model)
          : _HostDetail(row: row),
    );
  }
}

/// The filter's detail: the environments, then the agents, with the one in
/// force marked. Choosing either reads the hosts again.
class _FilterDetail extends StatelessWidget {
  const _FilterDetail({required this.model});

  final NetworkModel model;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final scope = model.scope;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Text('Environment', style: e.small.tint(e.muted)),
        const SizedBox(height: Space.s8),
        Rows([
          RowItem(
            name: 'Every environment',
            description: 'Hosts from every environment an agent ran in',
            value: scope.environment.isEmpty ? 'Current' : null,
            selected: scope.environment.isEmpty,
            onTap: () => model.filterTo(scope.withEnvironment('')),
          ),
          for (final environment in model.environments)
            RowItem(
              name: environment,
              description: 'Only what ran in $environment',
              value: scope.environment == environment ? 'Current' : null,
              selected: scope.environment == environment,
              onTap: () => model.filterTo(scope.withEnvironment(environment)),
            ),
        ]),
        const SizedBox(height: Space.s20),
        Text('Agent', style: e.small.tint(e.muted)),
        const SizedBox(height: Space.s8),
        Rows([
          RowItem(
            name: 'Every agent',
            description: 'Hosts from every agent',
            value: scope.agent.isEmpty ? 'Current' : null,
            selected: scope.agent.isEmpty,
            onTap: () => model.filterTo(scope.withAgent('')),
          ),
          for (final agent in model.agents)
            RowItem(
              name: agent,
              description: 'Only what $agent reached',
              value: scope.agent == agent ? 'Current' : null,
              selected: scope.agent == agent,
              onTap: () => model.filterTo(scope.withAgent(agent)),
            ),
        ]),
      ],
    );
  }
}

/// One host's detail: the rule that covers it and what the proxy logged.
class _HostDetail extends StatelessWidget {
  const _HostDetail({required this.row});

  final NetworkRow row;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Text(row.description, style: e.body.tint(e.muted)),
        const SizedBox(height: Space.s16),
        Box(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              for (final (term, meaning) in row.facts) _Fact(term, meaning),
            ],
          ),
        ),
      ],
    );
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
