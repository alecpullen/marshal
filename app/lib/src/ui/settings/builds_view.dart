import '../../model/environments_model.dart';
import '../../model/fleet_store.dart' show describeFailure;
import '../kit/kit.dart';

/// One template's builds (F7 decision 6): each version with its state, the
/// warm pool and the start times, an action to build, and the log of the
/// build that is chosen — streamed from its own event route.
class BuildsView extends StatefulWidget {
  const BuildsView({super.key, required this.model, required this.name});

  final EnvironmentsModel model;

  /// The template whose builds these are.
  final String name;

  @override
  State<BuildsView> createState() => _BuildsViewState();
}

class _BuildsViewState extends State<BuildsView> {
  EnvironmentBuilds? _builds;
  String? _error;
  var _loading = true;
  var _building = false;
  int? _open;
  BuildLog? _log;

  @override
  void initState() {
    super.initState();
    _load();
  }

  @override
  void dispose() {
    _log?.dispose();
    super.dispose();
  }

  Future<void> _load() async {
    setState(() {
      _loading = true;
      _error = null;
    });
    try {
      final builds = await widget.model.buildsOf(widget.name);
      if (!mounted) return;
      setState(() {
        _builds = builds;
        _loading = false;
      });
    } catch (error) {
      if (!mounted) return;
      setState(() {
        _error = describeFailure(error);
        _loading = false;
      });
    }
  }

  /// Watches [n]'s log from its first line.
  void _watch(int n) {
    _log?.dispose();
    final log = widget.model.openBuildLog(widget.name, n)..start();
    setState(() {
      _open = n;
      _log = log;
      _error = null;
    });
  }

  Future<void> _build() async {
    setState(() {
      _building = true;
      _error = null;
    });
    try {
      final n = await widget.model.startBuild(widget.name);
      if (!mounted) return;
      await _load();
      if (!mounted) return;
      _watch(n);
    } catch (error) {
      if (!mounted) return;
      setState(() => _error = describeFailure(error));
    } finally {
      if (mounted) setState(() => _building = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final builds = _builds;
    final error = _error;
    final versions = builds?.versions ?? const <EnvironmentVersion>[];
    final open = _open;
    final log = _log;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        if (_loading) const StateMessage.loading(lines: 3),
        if (error != null) ...[
          Text(error, style: e.body.tint(e.err)),
          const SizedBox(height: Space.s12),
        ],
        if (builds != null) ...[
          Text(
            'Warm pool ${builds.poolSize} · ${builds.poolIdle} idle · '
            '${builds.poolStarting} starting',
            style: e.small.tint(e.muted),
          ),
          const SizedBox(height: Space.s4),
          Text(
            builds.coldMs == 0 && builds.warmMs == 0
                ? 'No start has been timed yet'
                : 'Starts: cold ${_seconds(builds.coldMs)} · '
                      'warm ${_seconds(builds.warmMs)}',
            style: e.small.tint(e.muted),
          ),
          const SizedBox(height: Space.s12),
          if (versions.isEmpty)
            Text('No version has been built yet.', style: e.body.tint(e.muted))
          else
            Rows([
              for (final version in versions)
                RowItem(
                  name: 'v${version.n}',
                  description: version.detail,
                  value: version.state,
                  selected: version.n == open,
                  onTap: () => _watch(version.n),
                ),
            ]),
        ],
        if (open != null && log != null && log.lines.isNotEmpty) ...[
          const SizedBox(height: Space.s16),
          Text('Build v$open', style: e.small.tint(e.muted)),
          const SizedBox(height: Space.s8),
          AnimatedBuilder(
            animation: log,
            builder: (context, _) => OutputBlock(text: log.lines.join('\n')),
          ),
        ],
        const SizedBox(height: Space.s16),
        Align(
          alignment: Alignment.centerLeft,
          child: Btn.primary(
            _building ? 'Building' : 'Build the draft',
            onTap: _building ? null : _build,
          ),
        ),
      ],
    );
  }

  /// Milliseconds as the design system writes a duration.
  static String _seconds(int ms) =>
      ms < 1000 ? '${ms}ms' : '${(ms / 1000).round()}s';
}
