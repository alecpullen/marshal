import 'dart:async';

import 'package:flutter/services.dart';

import '../../model/preview_model.dart';
import '../kit/kit.dart';
import 'preview_frame.dart';

/// The Preview tab (f4-inspector.md decision 10): the ports the agent's
/// workspace declares, each loaded through the bridge's preview route.
///
/// In the browser the selected port is a frame. On a desktop it is a link and
/// a note saying why: an embedded desktop web view is not attempted in this
/// phase.
class PreviewTab extends StatefulWidget {
  const PreviewTab({super.key, required this.model, this.state});

  final PreviewModel model;

  /// The data state to draw instead of the ports (§7). Null is the ready
  /// state.
  final StateKind? state;

  @override
  State<PreviewTab> createState() => _PreviewTabState();
}

class _PreviewTabState extends State<PreviewTab> {
  int? _port;

  Future<void> _open(int port) async {
    setState(() => _port = port);
    await widget.model.open(port);
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

  /// The five states every screen that shows data has (§7).
  Widget _stateOnly(StateKind state) => switch (state) {
    StateKind.loading => const StateMessage.loading(lines: 4),
    StateKind.empty => StateMessage.empty(
      message: 'This agent’s workspace declares no preview ports.',
      action: StateAction('Read the ports again', onTap: () => setState(() {})),
    ),
    StateKind.stale => const StateMessage.stale(
      age: '4m',
      detail: 'The preview may be behind',
    ),
    StateKind.error => StateMessage.error(
      message: 'The preview could not be opened.',
      retry: StateAction('Try again', onTap: () => setState(() {})),
    ),
    StateKind.unavailable => StateMessage.unavailable(
      host: 'The bridge',
      lastSeen: '4m',
      action: StateAction('Try again', onTap: () => setState(() {})),
    ),
    StateKind.planned => const StateMessage.planned(
      name: 'A desktop web view',
      note: 'This phase opens the agent’s app in a browser',
    ),
  };

  Widget _body(BuildContext context) {
    final e = Ember.of(context);
    final ports = widget.model.ports;
    final port = _port;
    final url = port == null ? null : widget.model.urlFor(port);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Expanded(
          child: ListView(
            padding: const EdgeInsets.all(Space.s16),
            children: [
              if (ports.isEmpty)
                StateMessage.empty(
                  message: 'This agent’s workspace declares no preview ports.',
                  action: StateAction(
                    'Read the ports again',
                    onTap: () => setState(() {}),
                  ),
                )
              else
                Rows([
                  for (final port in ports)
                    RowItem(
                      name: 'port $port',
                      description: widget.model.loading(port)
                          ? 'Asking the bridge for its URL'
                          : (widget.model.errorFor(port) ??
                                'Declared by the workspace'),
                      value: widget.model.urlFor(port) == null ? null : 'Open',
                      mono: true,
                      selected: _port == port,
                      onTap: () => unawaited(_open(port)),
                    ),
                ]),
            ],
          ),
        ),
        if (port != null) ...[
          const Hairline(),
          Expanded(child: _preview(context, e, port, url)),
        ],
      ],
    );
  }

  Widget _preview(BuildContext context, Ember e, int port, String? url) {
    final error = widget.model.errorFor(port);
    if (error != null) {
      return StateMessage.error(
        message: error,
        retry: StateAction('Try again', onTap: () => unawaited(_open(port))),
      );
    }
    if (url == null) return const StateMessage.loading(lines: 3);
    if (previewFramesInApp) return previewFrame(url);
    return Padding(
      padding: const EdgeInsets.all(Space.s16),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text(url, style: e.mono),
          const SizedBox(height: Space.s8),
          Row(
            children: [
              Btn(
                'Copy link',
                icon: Glyphs.doc_on_doc,
                onTap: () =>
                    unawaited(Clipboard.setData(ClipboardData(text: url))),
              ),
            ],
          ),
          const SizedBox(height: Space.s8),
          Text(
            'A desktop window cannot show the agent’s app; open this link in '
            'a browser.',
            style: e.small.tint(e.muted),
          ),
        ],
      ),
    );
  }
}
