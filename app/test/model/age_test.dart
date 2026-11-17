import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/age.dart';

/// design-system.md §8: times are relative and short — "8m", "1h 12m", "2d".
void main() {
  final now = DateTime.utc(2026, 11, 17, 12);
  String age(Duration before) => relativeAge(now.subtract(before), now);

  test('ages are short and relative', () {
    expect(age(Duration.zero), 'now');
    expect(age(const Duration(seconds: 40)), 'now');
    expect(age(const Duration(minutes: 8)), '8m');
    expect(age(const Duration(minutes: 59)), '59m');
    expect(age(const Duration(hours: 1)), '1h');
    expect(age(const Duration(hours: 1, minutes: 12)), '1h 12m');
    expect(age(const Duration(hours: 23, minutes: 59)), '23h 59m');
    expect(age(const Duration(days: 2)), '2d');
    expect(age(const Duration(days: 40)), '40d');
  });

  test('a time ahead of now reads as now', () {
    expect(relativeAge(now.add(const Duration(minutes: 5)), now), 'now');
  });
}
