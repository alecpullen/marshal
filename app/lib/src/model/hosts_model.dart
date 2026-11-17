import 'fleet_store.dart' show HostView;
import 'section_model.dart';

/// Settings' Hosts section (F7 decision 7): every host discovery found, with
/// what this app can reach of each.
///
/// It is read-only. A host appears because discovery found it or because
/// `marshal fleet servers` lists it, and adding one is a planned row
/// (decision 9), so there is nothing here for a save to write.
class HostsModel extends SectionModel<HostView> {
  HostsModel(this._read);

  /// A model already holding [hosts], for a test or the gallery.
  HostsModel.fixture(List<HostView> hosts) : _read = (() => hosts) {
    replaceItems(hosts);
  }

  /// The fleet store's rows, so a host that stops answering keeps its place
  /// and is marked with its last contact rather than dropped (F2 decision 8).
  final List<HostView> Function() _read;

  @override
  Future<List<HostView>> fetch() async => _read();

  @override
  Object keyOf(HostView item) => item.name;

  @override
  bool isNew(HostView item) => false;

  /// Nothing to write: the detail of a host states what it is.
  @override
  Future<HostView> persist(HostView item) async => item;
}
