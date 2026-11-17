/// design-system.md §8: times are relative and short — "8m", "1h 12m", "2d".
/// The exact time belongs in the tooltip behind it, never in the line.
library;

/// How long before [now] something happened, in the design system's words.
///
/// A time later than [now] — a clock that disagrees with ours — reads as
/// "now" rather than as a negative age: nothing in the app is younger than
/// just now.
String relativeAge(DateTime at, DateTime now) {
  final elapsed = now.difference(at);
  if (elapsed.inMinutes < 1) return 'now';
  if (elapsed.inHours < 1) return '${elapsed.inMinutes}m';
  if (elapsed.inDays < 1) {
    final minutes = elapsed.inMinutes.remainder(60);
    return minutes == 0
        ? '${elapsed.inHours}h'
        : '${elapsed.inHours}h ${minutes}m';
  }
  return '${elapsed.inDays}d';
}
