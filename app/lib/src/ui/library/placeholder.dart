import '../kit/kit.dart';

/// The MCP servers a person configures in the app config: the bridge reports
/// no route for them yet, so the row is planned and says so
/// (design-system.md §7, F7 decision 9). It is never drawn as working.
RowItem plannedMcpServers() => const RowItem.planned(
  name: 'MCP servers',
  description:
      'The bridge lists no MCP servers yet; they are configured in the app '
      'config, and this row is where they will appear.',
);
