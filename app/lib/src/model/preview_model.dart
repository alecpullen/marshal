import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';

/// The agent's declared preview ports (f4-inspector.md decision 10).
///
/// The ports are the workspace's, declared under `[preview]`; the bridge
/// issues a URL for one at a time, and only for a port the workspace
/// declares. On a desktop the tab lists the ports and opens them in a
/// browser; in the browser it frames them.
class PreviewModel extends ChangeNotifier {
  PreviewModel(
    this._client, {
    required this.agentId,
    Iterable<int> ports = const [],
  }) : _ports = [...ports];

  /// A model holding what a caller gives it, for a test or the gallery.
  PreviewModel.seeded({
    Iterable<int> ports = const [],
    Map<int, String> urls = const {},
  }) : _client = null,
       agentId = '',
       _ports = [...ports] {
    _urls.addAll(urls);
  }

  final BridgeClient? _client;
  final String agentId;
  final List<int> _ports;
  final Map<int, String> _urls = {};
  final Map<int, String> _errors = {};
  final Set<int> _loading = {};

  /// The ports the workspace declares, in its order.
  List<int> get ports => List.unmodifiable(_ports);

  /// Replaces them: what the workspace doc says.
  void setPorts(Iterable<int> ports) {
    _ports
      ..clear()
      ..addAll(ports);
    notifyListeners();
  }

  bool loading(int port) => _loading.contains(port);

  /// The preview URL the bridge issued for [port], once it has.
  String? urlFor(int port) => _urls[port];

  /// Why [port] could not be opened, or null.
  String? errorFor(int port) => _errors[port];

  /// Asks the bridge for [port]'s preview URL. A port the workspace does not
  /// declare is refused by the bridge.
  Future<String?> open(int port) async {
    final issued = _urls[port];
    if (issued != null) return issued;
    final client = _client;
    if (client == null) return null;
    _loading.add(port);
    _errors.remove(port);
    notifyListeners();
    try {
      final url = await client.preview(agentId, port);
      _urls[port] = url;
      return url;
    } catch (error) {
      _errors[port] = '$error';
      return null;
    } finally {
      _loading.remove(port);
      notifyListeners();
    }
  }
}
