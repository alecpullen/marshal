import 'dart:async';

import '../../model/new_project_model.dart';
import '../kit/kit.dart';

/// The sheet the plus beside *Projects* opens (docs/studio-flutter/
/// new-project.md): make a project on a host — blank, cloned, or a folder the
/// host already has.
///
/// [onCreated] is told the host and folder once the host has registered the
/// project, and the sheet closes. A clone runs on the host whether or not the
/// sheet stays open; closing it only stops the sheet watching.
Future<void> showNewProjectSheet(
  BuildContext context,
  NewProjectModel model, {
  void Function(String host, String root)? onCreated,
}) => Sheet.show<void>(
  context,
  Sheet(
    title: 'Add a project',
    child: NewProjectForm(model: model, onCreated: onCreated),
  ),
);

/// The sheet's body: where, which way, and what git is doing.
class NewProjectForm extends StatefulWidget {
  const NewProjectForm({super.key, required this.model, this.onCreated});

  final NewProjectModel model;
  final void Function(String host, String root)? onCreated;

  @override
  State<NewProjectForm> createState() => _NewProjectFormState();
}

class _NewProjectFormState extends State<NewProjectForm> {
  late final _url = TextEditingController(text: widget.model.url);
  late final _name = TextEditingController(text: widget.model.name);
  late final _path = TextEditingController(text: widget.model.path);
  late final _branch = TextEditingController(text: widget.model.branch);

  /// Whether [NewProjectForm.onCreated] has been told, so a notify after the
  /// project is made does not tell it twice.
  bool _told = false;

  @override
  void initState() {
    super.initState();
    widget.model.addListener(_changed);
    unawaited(widget.model.load());
  }

  @override
  void dispose() {
    widget.model.removeListener(_changed);
    for (final controller in [_url, _name, _path, _branch]) {
      controller.dispose();
    }
    super.dispose();
  }

  /// The model fills the name and the folder as the URL and the name change;
  /// the fields follow it, and are told once the project is made.
  void _changed() {
    if (!mounted) return;
    final model = widget.model;
    for (final (controller, text) in [
      (_name, model.name),
      (_path, model.path),
    ]) {
      if (controller.text != text) controller.text = text;
    }
    final created = model.created;
    if (created != null && !_told) {
      _told = true;
      widget.onCreated?.call(model.host, created);
      unawaited(Navigator.of(context).maybePop());
    }
    setState(() {});
  }

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final model = widget.model;
    final working = model.busy;
    final way = model.way;
    final loadFailure = model.loadFailure;
    final failure = model.failure;
    final progress = model.progress;
    final problem = model.problem;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      mainAxisSize: MainAxisSize.min,
      children: [
        if (model.hosts.length > 1) ...[
          Text('Host', style: e.small.tint(e.muted)),
          const SizedBox(height: Space.s6),
          Align(
            alignment: Alignment.centerLeft,
            child: Menu(
              choices: [
                for (final host in model.hosts)
                  MenuChoice(
                    host,
                    host == model.host
                        ? 'The host the project is made on'
                        : 'Make the project on $host',
                    onSelect: () => unawaited(model.selectHost(host)),
                  ),
              ],
              anchor: (context, toggle) => Btn(
                model.host,
                icon: Glyphs.chevron_down,
                onTap: working ? null : toggle,
              ),
            ),
          ),
          const SizedBox(height: Space.s16),
        ],
        Wrap(
          spacing: Space.s8,
          runSpacing: Space.s4,
          children: [
            for (final choice in NewProjectWay.values)
              Tip(
                message: choice.note,
                child: choice == way
                    ? Btn.primary(
                        choice.word,
                        onTap: working ? null : () => model.selectWay(choice),
                      )
                    : Btn(
                        choice.word,
                        onTap: working ? null : () => model.selectWay(choice),
                      ),
              ),
          ],
        ),
        const SizedBox(height: Space.s16),
        if (way == NewProjectWay.clone) ...[
          Field(
            label: 'Repository URL',
            hint: 'https://github.com/acme/marshal.git',
            controller: _url,
            enabled: !working,
            onChanged: model.setUrl,
          ),
          const SizedBox(height: Space.s16),
        ],
        if (way != NewProjectWay.existing) ...[
          Field(
            label: 'Name',
            hint: way == NewProjectWay.clone ? 'marshal' : 'my-project',
            controller: _name,
            enabled: !working,
            onChanged: model.setName,
          ),
          const SizedBox(height: Space.s16),
        ],
        Field(
          label: way == NewProjectWay.existing
              ? 'Folder on ${model.host}'
              : 'Location on ${model.host}',
          hint: model.projectsDir == null
              ? '/home/you/projects/my-project'
              : '${model.projectsDir}/…',
          controller: _path,
          enabled: !working,
          mono: true,
          onChanged: model.setPath,
        ),
        if (way == NewProjectWay.clone) ...[
          const SizedBox(height: Space.s16),
          Field(
            label: 'Branch',
            hint: 'optional: the branch to check out',
            controller: _branch,
            enabled: !working,
            onChanged: model.setBranch,
          ),
        ],
        const SizedBox(height: Space.s12),
        Text(_explain(way, model.host), style: e.small.tint(e.muted)),
        if (model.loading) ...[
          const SizedBox(height: Space.s12),
          Text(
            'Reading ${model.host}’s projects folder…',
            style: e.small.tint(e.muted),
          ),
        ],
        if (loadFailure != null) ...[
          const SizedBox(height: Space.s12),
          StateMessage.error(
            message:
                '${model.host} did not say where its projects go: '
                '$loadFailure. Type the full folder instead.',
            retry: StateAction(
              'Ask again',
              onTap: () => unawaited(model.load()),
            ),
          ),
        ],
        if (working && way == NewProjectWay.clone) ...[
          const SizedBox(height: Space.s12),
          Text('Cloning on ${model.host}…', style: e.small),
          if (progress != null) ...[
            const SizedBox(height: Space.s4),
            Text(progress, style: e.mono.tint(e.muted)),
          ],
        ],
        if (failure != null) ...[
          const SizedBox(height: Space.s12),
          Text(failure, style: e.small.tint(e.err)),
        ],
        const SizedBox(height: Space.s20),
        Row(
          children: [
            Tip(
              message: problem ?? _does(way, model.host),
              child: Btn.primary(
                _verb(way, working),
                onTap: model.canCreate ? () => unawaited(model.create()) : null,
              ),
            ),
            const SizedBox(width: Space.s8),
            Btn(
              working ? 'Close' : 'Cancel',
              onTap: () => unawaited(Navigator.of(context).maybePop()),
            ),
          ],
        ),
      ],
    );
  }

  static String _verb(NewProjectWay way, bool working) => switch (way) {
    NewProjectWay.blank => working ? 'Creating…' : 'Create',
    NewProjectWay.clone => working ? 'Cloning…' : 'Clone',
    NewProjectWay.existing => working ? 'Adding…' : 'Add',
  };

  static String _does(NewProjectWay way, String host) => switch (way) {
    NewProjectWay.blank => 'Make the folder and an empty repository on $host',
    NewProjectWay.clone => 'Clone the repository on $host',
    NewProjectWay.existing => 'Add the folder on $host as a project',
  };

  static String _explain(NewProjectWay way, String host) => switch (way) {
    NewProjectWay.blank =>
      'An empty git repository, with no commit yet. The folder must be new '
          'or empty.',
    NewProjectWay.clone =>
      '$host clones with its own git setup — its SSH keys and credential '
          'helpers. Closing this sheet does not stop the clone.',
    NewProjectWay.existing =>
      'The folder is added as it is: nothing in it is changed.',
  };
}
