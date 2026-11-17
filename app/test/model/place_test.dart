import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/place.dart';

/// The places are the URL path under the studio's base (F2 decision 4), so
/// the parser and the formatter must be one inverse pair: a reload, a Back
/// button and a pasted deep link all land on the place they name.
void main() {
  const everyPlace = [
    Place.overview(),
    Place(PlaceKind.inbox),
    Place(PlaceKind.pulls),
    Place(PlaceKind.automations),
    Place(PlaceKind.library),
    Place(PlaceKind.settings),
    Place(PlaceKind.project, id: '/home/u/alpha'),
    Place(PlaceKind.thread, host: 'local', id: 'a-1'),
  ];

  test('place round trips', () {
    for (final place in everyPlace) {
      final path = formatPlace(place);
      expect(parsePlace(path), place, reason: path);
    }
  });

  test('the paths are the ones the design names', () {
    expect(formatPlace(const Place.overview()), '/');
    expect(formatPlace(const Place(PlaceKind.inbox)), '/inbox');
    expect(formatPlace(const Place(PlaceKind.pulls)), '/pulls');
    expect(formatPlace(const Place(PlaceKind.automations)), '/automations');
    expect(formatPlace(const Place(PlaceKind.library)), '/library');
    expect(formatPlace(const Place(PlaceKind.settings)), '/settings');
    expect(
      formatPlace(const Place(PlaceKind.project, id: '/home/u/alpha')),
      '/project/%2Fhome%2Fu%2Falpha',
      reason: 'a project id is a path, so it is encoded into one segment',
    );
    expect(
      formatPlace(const Place(PlaceKind.thread, host: 'local', id: 'a-1')),
      '/thread/local/a-1',
    );
  });

  test('unknown path is overview', () {
    for (final path in const [
      '/nothing',
      '/inbox/extra',
      '/project',
      '/thread/local/a-1/step',
      '/pulls/today',
    ]) {
      expect(parsePlace(path), const Place.overview(), reason: path);
    }
    // A malformed escape cannot name a place either.
    expect(parsePlace('/project/%ZZ'), const Place.overview());
  });

  test('a thread named without its host is still the thread', () {
    // A link written for the one-bridge Studio named a thread and no host
    // (F8 decision 2); the shell asks the fleet which host answers for it.
    const place = Place(PlaceKind.thread, id: 'a-1');

    expect(parsePlace('/thread/a-1'), place);
    expect(formatPlace(place), '/thread/a-1');
    expect(
      everyPlace,
      isNot(contains(place)),
      reason: 'the round-trip list names hosts, as this build writes them',
    );
  });

  test('a trailing slash, a query and a fragment are ignored', () {
    expect(parsePlace('/inbox/'), const Place(PlaceKind.inbox));
    expect(parsePlace('/inbox?filter=blocking'), const Place(PlaceKind.inbox));
    expect(parsePlace('/thread/local/a-1#top'), everyPlace.last);
    expect(parsePlace(''), const Place.overview());
    expect(parsePlace('/'), const Place.overview());
  });
}
