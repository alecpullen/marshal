import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import 'section_model.dart';

// A client's own types reach their screens through the model, so `ui/` reads
// `model/` and never `client/` (test/boundary_test.dart).
export '../client/contract.g.dart'
    show
        ApprovalAuthority,
        CreateClientResult,
        FactoryGrantState,
        MCPClient,
        MCPFactoryProjectGrant;

/// The MCP tab's clients and their factory grants (F7 decision 5).
///
/// A client's token is shown once, by [create], and never stored here: the
/// section has no way to read one back because the bridge never sends one
/// again.
class ClientsModel extends SectionModel<MCPClient> {
  ClientsModel(this._bridge, {this.projects = const []}) : _fixture = null;

  /// A section with rows and no bridge, for the gallery and the screens the
  /// enforcement tests pump.
  ClientsModel.fixture(List<MCPClient> rows, {this.projects = const []})
    : _bridge = null,
      _fixture = rows;

  final BridgeClient? _bridge;
  final List<MCPClient>? _fixture;

  /// The project ids a grant can be written against, as the fleet reports
  /// them.
  final List<String> projects;

  @override
  Future<List<MCPClient>> fetch() async =>
      _fixture ?? await _requireBridge().clients();

  @override
  Object keyOf(MCPClient client) => client.id;

  @override
  bool isNew(MCPClient client) => false;

  @override
  Future<MCPClient> persist(MCPClient client) =>
      throw StateError('a client is registered through its create form');

  /// The row a "New client" action opens: an empty one, whose detail is the
  /// create form.
  static MCPClient blank() =>
      const MCPClient(id: '', name: '', ownerID: '', createdAt: '');

  /// POST /api/clients: registers a client and answers its token, which is
  /// the only time the bridge sends one.
  Future<CreateClientResult> create(String name) async {
    final created = await _requireBridge().createClient(
      CreateClientRequest(name: name),
    );
    await load();
    return created;
  }

  /// DELETE /api/clients/{id}.
  Future<void> remove(MCPClient client) async {
    await _requireBridge().deleteClient(client.id);
    removed(client);
  }

  /// GET one pair's grant, with the version a write must name.
  Future<FactoryGrantState> grant(MCPClient client, String projectID) =>
      _requireBridge().factoryGrant(client.id, projectID);

  /// PUT the whole grant plus the version it was edited from. The bridge
  /// refuses a stale version with 409 and sends the state that won, which
  /// [SectionModel] would keep: a grant editor shows that refusal in place.
  Future<FactoryGrantState> saveGrant(
    MCPClient client,
    FactoryGrantState state,
    MCPFactoryProjectGrant grant,
  ) => _requireBridge().putFactoryGrant(
    client.id,
    state.projectID,
    PutGrantRequest(grant: grant, version: state.version),
  );

  BridgeClient _requireBridge() {
    final bridge = _bridge;
    if (bridge == null) {
      throw StateError('a fixture section has no bridge');
    }
    return bridge;
  }
}
