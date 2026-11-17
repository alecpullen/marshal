import '../../model/fleet_store.dart' show describeFailure;
import '../../model/library_model.dart';
import '../kit/kit.dart';
import '../sections/section_page.dart';
import '../automations/confirm_action.dart';
import 'library_place.dart' show LibraryTabs;

/// The Plugins tab of Library (F7 decision 5): the installed plugins of one
/// scope. Adding one goes through a scan that says what it ships, and only
/// the scan's Confirm installs it.
class PluginsTab extends StatelessWidget {
  const PluginsTab({super.key, required this.model, this.tabs, this.state});

  final PluginsModel model;
  final LibraryTabs? tabs;
  final StateKind? state;

  @override
  Widget build(BuildContext context) {
    return SectionPage<PluginEntry>(
      title: 'Plugins',
      subtitle: 'Skill, command and hook bundles an agent can use',
      model: model,
      tabs: tabs?.items,
      tabIndex: tabs?.index ?? 0,
      onTab: tabs?.onChanged,
      state: state,
      readOnly: true,
      createLabel: 'Scan a plugin',
      blank: PluginsModel.blank,
      detailTitle: (plugin) =>
          plugin.name.isEmpty ? 'Scan a plugin' : plugin.name,
      emptyMessage:
          'A plugin you scan appears here. Nothing is installed until its '
          'scan is confirmed.',
      row: (plugin, onTap) => RowItem(
        name: plugin.name,
        description: plugin.source,
        value: plugin.scope,
        onTap: onTap,
      ),
      detail: (context, plugin) => plugin.name.isEmpty
          ? _Scan(model: model)
          : _Installed(model: model, plugin: plugin),
    );
  }
}

/// The scan form: a source and a ref, the scan itself, and then Confirm or
/// Discard.
class _Scan extends StatefulWidget {
  const _Scan({required this.model});

  final PluginsModel model;

  @override
  State<_Scan> createState() => _ScanState();
}

class _ScanState extends State<_Scan> {
  final _source = TextEditingController();
  final _ref = TextEditingController();
  PluginScan? _scan;
  var _busy = false;
  String? _error;

  @override
  void dispose() {
    _source.dispose();
    _ref.dispose();
    super.dispose();
  }

  Future<void> _stage() async {
    final source = _source.text.trim();
    if (source.isEmpty) {
      setState(() => _error = 'Name a source first.');
      return;
    }
    setState(() {
      _busy = true;
      _error = null;
      _scan = null;
    });
    try {
      final scan = await widget.model.scan(source, ref: _ref.text.trim());
      if (!mounted) return;
      setState(() {
        _busy = false;
        _scan = scan;
      });
    } catch (error) {
      if (!mounted) return;
      setState(() {
        _busy = false;
        _error = describeFailure(error);
      });
    }
  }

  Future<void> _confirm() async {
    final scan = _scan!;
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      await widget.model.confirm(scan);
    } catch (error) {
      if (!mounted) return;
      setState(() {
        _busy = false;
        _error = describeFailure(error);
      });
      return;
    }
    if (!mounted) return;
    Navigator.of(context).maybePop();
  }

  Future<void> _discard() async {
    final scan = _scan!;
    try {
      await widget.model.discard(scan);
    } catch (error) {
      if (!mounted) return;
      setState(() => _error = describeFailure(error));
      return;
    }
    if (!mounted) return;
    setState(() => _scan = null);
  }

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final scan = _scan;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Text(
          'A plugin is scanned before it is installed: what it ships is '
          'shown, and nothing is installed until you confirm it.',
          style: e.small.tint(e.muted),
        ),
        const SizedBox(height: Space.s16),
        Field(
          label: 'Source',
          controller: _source,
          hint: 'github.com/acme/tools',
        ),
        const SizedBox(height: Space.s12),
        Field(label: 'Ref', controller: _ref, hint: 'v1, or a branch'),
        const SizedBox(height: Space.s12),
        Align(
          alignment: Alignment.centerLeft,
          child: Btn(_busy ? 'Scanning' : 'Scan', onTap: _busy ? null : _stage),
        ),
        if (scan != null) ...[
          const SizedBox(height: Space.s20),
          _Fact(label: 'Name', value: scan.name),
          _Fact(label: 'Source', value: scan.source),
          _Fact(
            label: 'Commit',
            value: scan.ref == null || scan.ref!.isEmpty
                ? scan.commit
                : '${scan.commit} (${scan.ref})',
          ),
          _Fact(label: 'Ships', value: _contents(scan.contents)),
          const SizedBox(height: Space.s12),
          Wrap(
            spacing: Space.s8,
            runSpacing: Space.s8,
            children: [
              Btn.primary(
                _busy ? 'Installing' : 'Confirm',
                onTap: _busy ? null : _confirm,
              ),
              Btn('Discard', onTap: _busy ? null : _discard),
            ],
          ),
        ],
        if (_error != null) ...[
          const SizedBox(height: Space.s12),
          Text(_error!, style: e.small.tint(e.err)),
        ],
      ],
    );
  }

  /// What the scan says the plugin ships, in a line.
  static String _contents(PluginContents contents) {
    final parts = <String>[
      if (contents.skillCount > 0)
        '${contents.skillCount} ${contents.skillCount == 1 ? 'skill' : 'skills'}',
      if (contents.commandCount > 0)
        '${contents.commandCount} '
            '${contents.commandCount == 1 ? 'command' : 'commands'}',
      if (contents.hookCount > 0)
        '${contents.hookCount} ${contents.hookCount == 1 ? 'hook' : 'hooks'}',
      if (contents.mcpServerCount > 0)
        '${contents.mcpServerCount} '
            '${contents.mcpServerCount == 1 ? 'MCP server' : 'MCP servers'}',
      if (contents.mcpPolicyCount > 0)
        '${contents.mcpPolicyCount} MCP '
            '${contents.mcpPolicyCount == 1 ? 'policy' : 'policies'}',
    ];
    if (parts.isEmpty) {
      return contents.hasManifest ? 'A manifest only' : 'Nothing';
    }
    return parts.join(' · ');
  }
}

/// An installed plugin: what it is, and removing it behind a confirmation.
class _Installed extends StatefulWidget {
  const _Installed({required this.model, required this.plugin});

  final PluginsModel model;
  final PluginEntry plugin;

  @override
  State<_Installed> createState() => _InstalledState();
}

class _InstalledState extends State<_Installed> {
  var _confirming = false;
  String? _error;

  Future<void> _remove() async {
    setState(() => _error = null);
    try {
      await widget.model.remove(widget.plugin);
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
    final plugin = widget.plugin;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        _Fact(label: 'Source', value: plugin.source),
        _Fact(label: 'Commit', value: plugin.commit),
        _Fact(
          label: 'Installed',
          value: plugin.installedAt.isEmpty
              ? 'Before this build'
              : plugin.installedAt,
        ),
        _Fact(label: 'Scope', value: plugin.scope),
        if (_error != null) ...[
          const SizedBox(height: Space.s12),
          Text(_error!, style: e.small.tint(e.err)),
        ],
        const SizedBox(height: Space.s20),
        if (_confirming)
          ConfirmAction(
            question: 'Remove ${plugin.name}?',
            loss:
                'The plugin and everything it installed in this scope are '
                'deleted. A copy elsewhere stays.',
            confirmLabel: 'Remove it',
            onConfirm: _remove,
            onCancel: () => setState(() => _confirming = false),
          )
        else
          Align(
            alignment: Alignment.centerLeft,
            child: Btn(
              'Remove this plugin',
              onTap: () => setState(() => _confirming = true),
            ),
          ),
      ],
    );
  }
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
