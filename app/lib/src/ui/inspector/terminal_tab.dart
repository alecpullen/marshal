import 'dart:async';

import '../../model/terminal_session.dart';
import '../kit/kit.dart';
import 'inspector.dart';
import 'terminal_view.dart';

/// The Terminal tab (f4-inspector.md decision 9): a shell in the agent's
/// workspace, and the hold that typing puts on the agent.
///
/// The first keystroke pauses the agent; the footer says so and offers Hand
/// back, which resumes it — as does two minutes without input.
class TerminalTab extends StatefulWidget {
  const TerminalTab({super.key, required this.session, this.state});

  final TerminalSession session;

  /// The data state to draw instead of the shell (§7). Null is the ready
  /// state.
  final StateKind? state;

  @override
  State<TerminalTab> createState() => _TerminalTabState();
}

class _TerminalTabState extends State<TerminalTab> {
  final _terminal = newTerminal();
  StreamSubscription<String>? _output;

  @override
  void initState() {
    super.initState();
    _terminal
      ..onOutput = widget.session.input
      // xterm reports the size in cells and in pixels; the shell needs the
      // cells.
      ..onResize = (width, height, _, _) =>
          widget.session.resize(width, height);
    _output = widget.session.output.listen(_terminal.write);
    unawaited(widget.session.open());
  }

  @override
  void dispose() {
    unawaited(_output?.cancel());
    _terminal.onOutput = null;
    _terminal.onResize = null;
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: widget.session,
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
      message: 'This agent has no shell open.',
      action: StateAction(
        'Open a shell',
        onTap: () => unawaited(widget.session.open()),
      ),
    ),
    StateKind.stale => const StateMessage.stale(
      age: '4m',
      detail: 'The shell may be behind',
    ),
    StateKind.error => StateMessage.error(
      message: 'The shell could not be opened.',
      retry: StateAction(
        'Try again',
        onTap: () => unawaited(widget.session.open()),
      ),
    ),
    StateKind.unauthorized => const StateMessage.unauthorized(),
    StateKind.unavailable => StateMessage.unavailable(
      host: 'The bridge',
      lastSeen: '4m',
      action: StateAction(
        'Try again',
        onTap: () => unawaited(widget.session.open()),
      ),
    ),
    StateKind.planned => const StateMessage.planned(
      name: 'A command’s replay',
      note: 'Showing a finished command above the live shell is not built yet',
    ),
  });

  Widget _body(BuildContext context) {
    // The bridge names why a shell never started on the chunk that ends its
    // stream (decision 9): without this the tab would show an empty terminal
    // for a shell that failed before it could print anything.
    final error = widget.session.error;
    if (error != null) {
      return TabMessage(
        StateMessage.error(
          message: 'The shell could not be opened: $error',
          retry: StateAction(
            'Try again',
            onTap: () => unawaited(widget.session.reopen()),
          ),
        ),
      );
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        // The terminal paints its own background from the theme.
        Expanded(child: TerminalPane(terminal: _terminal)),
        const Hairline(),
        _footer(context),
      ],
    );
  }

  /// The pause rule, and the way out of it.
  Widget _footer(BuildContext context) {
    final e = Ember.of(context);
    final paused = widget.session.agentPaused;
    return Padding(
      padding: const EdgeInsets.all(Space.s12),
      child: Row(
        children: [
          Expanded(
            child: Text(
              paused
                  ? 'The agent is paused while you type here.'
                  : 'Typing here pauses the agent until you hand back.',
              style: e.small.tint(e.muted),
              maxLines: 2,
            ),
          ),
          if (paused) ...[
            const SizedBox(width: Space.s8),
            Btn('Hand back', onTap: () => unawaited(widget.session.handBack())),
          ],
        ],
      ),
    );
  }
}
