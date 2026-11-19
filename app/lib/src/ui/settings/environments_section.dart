import '../../model/environments_model.dart';
import '../../model/fleet_store.dart' show describeFailure;
import '../kit/kit.dart';
import '../sections/detail_host.dart';
import '../sections/section_page.dart';
import 'builds_view.dart';

/// Settings' Environments section (F7 decision 6): the gallery of workspace
/// templates — name, contents in one line, version and use — with the builds
/// behind each and the import that starts a draft.
class EnvironmentsSection extends StatelessWidget {
  const EnvironmentsSection({
    super.key,
    required this.model,
    this.state,
    this.designer,
  });

  final EnvironmentsModel model;

  /// The state the shell knows, when it knows more than the model does.
  final StateKind? state;

  /// Builds the designer for one template (F7 decision 6). Null leaves the
  /// detail as what the template is, with its builds.
  final Widget Function(EnvironmentRow row)? designer;

  @override
  Widget build(BuildContext context) {
    final designer = this.designer;
    return SectionPage<EnvironmentRow>(
      title: 'Environments',
      subtitle: 'The containers agents run in, and the builds behind them',
      model: model,
      state: state,
      readOnly: true,
      emptyMessage:
          'No environment exists yet. Import a devcontainer to make '
          'one.',
      actions: [Btn('Import devcontainer.json', onTap: () => _import(context))],
      emptyAction: StateAction(
        'Import devcontainer.json',
        onTap: () => _import(context),
      ),
      row: (row, onTap) => RowItem(
        name: row.name,
        description: row.contents,
        value: row.value,
        onTap: onTap,
      ),
      detail: (context, row) => designer == null
          ? _TemplateDetail(model: model, row: row)
          : designer(row),
    );
  }

  /// Starts a draft from a `devcontainer.json`: the person names the template
  /// and the path the bridge reads it from.
  Future<void> _import(BuildContext context) async {
    final name = TextEditingController();
    final path = TextEditingController();
    String? error;
    var importing = false;
    await Sheet.show<void>(
      context,
      Sheet(
        title: 'Import a devcontainer',
        actions: [Btn('Cancel', onTap: () => Navigator.maybePop(context))],
        child: StatefulBuilder(
          builder: (context, setState) => Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Field(
                label: 'Name',
                hint: 'go-service',
                controller: name,
                onChanged: (_) => setState(() => error = null),
              ),
              const SizedBox(height: Space.s16),
              Field(
                label: 'Devcontainer',
                hint: 'acme/widgets/.devcontainer/devcontainer.json',
                controller: path,
                onChanged: (_) => setState(() => error = null),
              ),
              if (error != null) ...[
                const SizedBox(height: Space.s12),
                Text(
                  error!,
                  style: Ember.of(context).body.tint(Ember.of(context).err),
                ),
              ],
              const SizedBox(height: Space.s16),
              Align(
                alignment: Alignment.centerLeft,
                child: Btn.primary(
                  importing ? 'Importing' : 'Import',
                  onTap: importing
                      ? null
                      : () async {
                          setState(() {
                            importing = true;
                            error = null;
                          });
                          try {
                            await model.importDevcontainer(
                              name.text,
                              path.text,
                            );
                            if (context.mounted) Navigator.maybePop(context);
                          } catch (failure) {
                            if (!context.mounted) return;
                            setState(() {
                              importing = false;
                              error = describeFailure(failure);
                            });
                          }
                        },
                ),
              ),
            ],
          ),
        ),
      ),
    );
    name.dispose();
    path.dispose();
  }
}

/// One template's detail: what it is, and the builds behind it.
class _TemplateDetail extends StatelessWidget {
  const _TemplateDetail({required this.model, required this.row});

  final EnvironmentsModel model;
  final EnvironmentRow row;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final loaded = model.loadedOf(row.name);
    final diagnostics = loaded?.diagnostics ?? const [];
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        _Fact('Contents', row.contents),
        _Fact(
          'Source',
          row.source == 'repo'
              ? 'A repository template, in ${row.project}'
              : 'A Studio template',
        ),
        _Fact(
          'Published',
          row.version == 0 ? 'Not published yet' : 'v${row.version}',
        ),
        if (row.draftChanges)
          _Fact('Draft', 'Differs from the published version'),
        _Fact('Used by', EnvironmentRow.agents(row.usage)),
        if (diagnostics.isNotEmpty) ...[
          const SizedBox(height: Space.s12),
          Box(
            tone: BoxTone.attention,
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                for (final diagnostic in diagnostics)
                  Text(
                    'Line ${diagnostic.line}: ${diagnostic.message}',
                    style: e.small,
                  ),
              ],
            ),
          ),
        ],
        const SizedBox(height: Space.s16),
        Align(
          alignment: Alignment.centerLeft,
          child: Btn(
            'Builds',
            onTap: () => openDetail(
              context,
              title: 'Builds of ${row.name}',
              body: BuildsView(model: model, name: row.name),
            ),
          ),
        ),
      ],
    );
  }
}

/// One named fact of a template.
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
