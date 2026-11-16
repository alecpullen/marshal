import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import 'section_model.dart';

// A section's own type reaches its screens through the model, so `ui/` reads
// `model/` and never `client/` (test/boundary_test.dart).
export '../client/contract.g.dart' show Schedule;

/// The Schedules section of Automations (F7 decision 4): the cron entries
/// that start a recipe without anyone asking.
///
/// It extends [SectionModel], so save, stale-save and unsaved-edit behaviour
/// are the shared ones; what it adds is the two actions a schedule has beyond
/// an edit — run it now, and delete it.
class SchedulesModel extends SectionModel<Schedule> {
  SchedulesModel(this._bridge) : _fixture = null;

  /// A section with rows and no bridge, for the gallery and the screens the
  /// enforcement tests pump. A write on it says so rather than pretending.
  SchedulesModel.fixture(List<Schedule> rows) : _bridge = null, _fixture = rows;

  final BridgeClient? _bridge;
  final List<Schedule>? _fixture;

  /// A schedule nobody has filled in yet: the bridge assigns its id, so an
  /// empty one is what a save creates.
  static Schedule blank() => const Schedule(
    id: '',
    name: '',
    recipe: '',
    cron: '',
    enabled: true,
    ownerID: '',
  );

  @override
  Future<List<Schedule>> fetch() async =>
      _fixture ?? await _requireBridge().schedules();

  @override
  Object keyOf(Schedule schedule) => schedule.id;

  @override
  bool isNew(Schedule schedule) =>
      schedule.id.isEmpty || !items.any((held) => held.id == schedule.id);

  @override
  Future<Schedule> persist(Schedule schedule) =>
      _requireBridge().saveSchedule(schedule);

  /// POST /api/schedules/{id}/run: starts the schedule now. The bridge
  /// refuses with 409 while the previous run is still going, and the caller
  /// says so in the bridge's own words.
  Future<void> runNow(Schedule schedule) =>
      _requireBridge().runSchedule(schedule.id);

  /// DELETE /api/schedules/{id}, then drop the row: the list is what the
  /// bridge said it holds, never a guess.
  Future<void> delete(Schedule schedule) async {
    await _requireBridge().deleteSchedule(schedule.id);
    removed(schedule);
  }

  BridgeClient _requireBridge() {
    final bridge = _bridge;
    if (bridge == null) {
      throw StateError('a fixture section has no bridge');
    }
    return bridge;
  }
}
