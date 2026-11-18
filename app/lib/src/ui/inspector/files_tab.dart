import 'dart:async';

import 'package:re_highlight/languages/bash.dart';
import 'package:re_highlight/languages/dart.dart';
import 'package:re_highlight/languages/go.dart';
import 'package:re_highlight/languages/json.dart';
import 'package:re_highlight/languages/markdown.dart';
import 'package:re_highlight/languages/typescript.dart';
import 'package:re_highlight/languages/yaml.dart';
import 'package:re_highlight/re_highlight.dart';

import '../../model/files_model.dart';
import '../../model/fleet_store.dart' show describeFailure;
import '../kit/kit.dart';
import 'inspector.dart';

/// The Files tab (f4-inspector.md decision 8): a lazily loaded tree of the
/// agent's workspace, each file this thread changed marked, and a read-only
/// viewer with syntax highlighting.
///
/// The tree asks for a directory when it is opened and never before. The
/// viewer tokenises with `re_highlight`; the colours are the fixed class map
/// in `tokens.dart`, so a token class the map does not name is `fg`.
class FilesTab extends StatefulWidget {
  const FilesTab({super.key, required this.model, this.state});

  final FilesModel model;

  /// The data state to draw instead of the tree (§7). Null is the ready
  /// state.
  final StateKind? state;

  @override
  State<FilesTab> createState() => _FilesTabState();
}

class _FilesTabState extends State<FilesTab> {
  String? _selected;
  FileText? _view;
  bool _loading = false;
  String? _failure;

  @override
  void initState() {
    super.initState();
    unawaited(widget.model.expand(''));
  }

  Future<void> _open(String path) async {
    setState(() {
      _selected = path;
      _loading = true;
      _failure = null;
    });
    try {
      final view = await widget.model.open(path);
      if (!mounted) return;
      setState(() {
        _view = view;
        _loading = false;
      });
    } catch (error) {
      if (!mounted) return;
      setState(() {
        _failure = describeFailure(error);
        _loading = false;
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: widget.model,
      builder: (context, _) {
        final state = widget.state;
        return state == null ? _body(context) : _stateOnly(state);
      },
    );
  }

  /// The five states every screen that shows data has (§7), inset as the
  /// ready content is.
  Widget _stateOnly(StateKind state) => TabMessage(switch (state) {
    StateKind.loading => const StateMessage.loading(lines: 6),
    StateKind.empty => StateMessage.empty(
      message: 'The workspace has no files to show.',
      action: StateAction(
        'Read the tree again',
        onTap: () => unawaited(widget.model.expand('')),
      ),
    ),
    StateKind.stale => const StateMessage.stale(
      age: '4m',
      detail: 'The tree may be behind',
    ),
    StateKind.error => StateMessage.error(
      message: 'The workspace could not be read.',
      retry: StateAction(
        'Try again',
        onTap: () => unawaited(widget.model.expand('')),
      ),
    ),
    StateKind.unauthorized => const StateMessage.unauthorized(),
    StateKind.unavailable => StateMessage.unavailable(
      host: 'The bridge',
      lastSeen: '4m',
      action: StateAction(
        'Try again',
        onTap: () => unawaited(widget.model.expand('')),
      ),
    ),
    StateKind.planned => const StateMessage.planned(
      name: 'Editing a file',
      note: 'This viewer is read-only',
    ),
  });

  Widget _body(BuildContext context) => Column(
    crossAxisAlignment: CrossAxisAlignment.stretch,
    children: [
      Expanded(child: _tree(context)),
      const Hairline(),
      Expanded(child: _viewer(context)),
    ],
  );

  Widget _tree(BuildContext context) {
    final model = widget.model;
    final rows = _rows();
    if (rows.isEmpty) {
      return model.errorFor('') == null
          ? const TabMessage(StateMessage.loading(lines: 4))
          : TabMessage(
              StateMessage.error(
                message: model.errorFor('')!,
                retry: StateAction(
                  'Try again',
                  onTap: () => unawaited(model.expand('')),
                ),
              ),
            );
    }
    return ListView(
      padding: const EdgeInsets.symmetric(vertical: Space.s8),
      children: [for (final row in rows) _row(context, row)],
    );
  }

  /// The rows the open directories make visible, depth first.
  List<_Row> _rows() {
    final out = <_Row>[];
    void walk(String path, int depth) {
      for (final entry in widget.model.children(path)) {
        final child = path.isEmpty ? entry.name : '$path/${entry.name}';
        out.add(_Row(child, entry, depth));
        if (entry.dir && widget.model.expanded(child)) walk(child, depth + 1);
      }
    }

    if (widget.model.expanded('')) walk('', 0);
    return out;
  }

  Widget _row(BuildContext context, _Row row) {
    final e = Ember.of(context);
    final entry = row.entry;
    final changed = widget.model.changed(row.path);
    final nav = NavRow(
      label: entry.name,
      icon: entry.dir ? Glyphs.folder : Glyphs.doc,
      mono: true,
      indent: row.depth > 0,
      selected: _selected == row.path,
      expanded: entry.dir ? widget.model.expanded(row.path) : null,
      onToggle: entry.dir
          ? () => unawaited(widget.model.toggle(row.path))
          : null,
      onTap: () => unawaited(
        entry.dir ? widget.model.toggle(row.path) : _open(row.path),
      ),
    );
    if (!changed) return nav;
    return Row(
      children: [
        Expanded(child: nav),
        Padding(
          padding: const EdgeInsets.only(right: Space.s12),
          child: Tip(
            message: 'Changed in this thread',
            child: Semantics(
              label: 'Changed in this thread',
              child: Icon(
                Glyphs.pencil,
                size: Sizes.iconInline,
                color: e.muted,
              ),
            ),
          ),
        ),
      ],
    );
  }

  Widget _viewer(BuildContext context) {
    final e = Ember.of(context);
    final path = _selected;
    if (path == null) {
      return StateMessage.empty(
        message: 'Open a file to read it.',
        action: StateAction(
          'Read the tree again',
          onTap: () => unawaited(widget.model.expand('')),
        ),
      );
    }
    if (_loading) return const StateMessage.loading(lines: 4);
    final failure = _failure;
    if (failure != null) {
      return StateMessage.error(
        message: failure,
        retry: StateAction('Try again', onTap: () => unawaited(_open(path))),
      );
    }
    final view = _view;
    if (view == null) return const StateMessage.loading(lines: 4);
    if (view.binary) {
      return const StateMessage.empty(
        message: 'This file is not text, so there is nothing to show.',
        action: StateAction('Close', onTap: _noop),
      );
    }
    final span = _highlighter.span(view.path, view.text, e);
    return SingleChildScrollView(
      padding: const EdgeInsets.all(Space.s12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text.rich(span ?? TextSpan(text: view.text), style: e.mono),
          if (view.truncated)
            Padding(
              padding: const EdgeInsets.only(top: Space.s8),
              child: Text(
                'The agent capped this file; the rest is not shown.',
                style: e.small.tint(e.muted),
              ),
            ),
        ],
      ),
    );
  }
}

/// One visible row of the tree: its path, its entry, and its depth.
@immutable
class _Row {
  const _Row(this.path, this.entry, this.depth);

  final String path;
  final dynamic entry;
  final int depth;
}

void _noop() {}

/// The one tokeniser: languages registered once, and a fixed class map.
class _Highlighter {
  final _highlight = Highlight();
  bool _ready = false;

  TextSpan? span(String path, String text, Ember e) {
    final language = _languageFor(path);
    if (language == null) return null;
    if (!_ready) {
      _highlight
        ..registerLanguage('dart', langDart)
        ..registerLanguage('go', langGo)
        ..registerLanguage('typescript', langTypescript)
        ..registerLanguage('json', langJson)
        ..registerLanguage('yaml', langYaml)
        ..registerLanguage('bash', langBash)
        ..registerLanguage('markdown', langMarkdown);
      _ready = true;
    }
    try {
      final result = _highlight.highlight(code: text, language: language);
      final renderer = TextSpanRenderer(e.mono, {
        for (final name in Ember.codeClasses)
          name: e.mono.tint(e.codeToken(name)),
      });
      result.render(renderer);
      return renderer.span;
    } catch (_) {
      // A file the tokeniser cannot read is still shown: plain, never
      // hidden.
      return null;
    }
  }

  static String? _languageFor(String path) {
    final name = path.toLowerCase();
    if (name.endsWith('.dart')) return 'dart';
    if (name.endsWith('.go')) return 'go';
    if (name.endsWith('.ts') || name.endsWith('.tsx')) return 'typescript';
    if (name.endsWith('.json')) return 'json';
    if (name.endsWith('.yaml') || name.endsWith('.yml')) return 'yaml';
    if (name.endsWith('.sh') || name.endsWith('.bash')) return 'bash';
    if (name.endsWith('.md')) return 'markdown';
    return null;
  }
}

final _highlighter = _Highlighter();
