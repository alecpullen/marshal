import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import 'support/screens.dart';

/// design-system.md §9 and §11: every icon-only control, status dot, chip
/// with an icon, bar and strip has a tooltip. Each registered screen is
/// pumped with its fixture data in every case at each of the three widths,
/// and its widget tree is walked:
///
/// - an icon inside a control (anything with a tap) whose control has no
///   words must sit under a `Tip` that holds no words of its own;
/// - an icon outside a control with no words beside it in its row — on a
///   line of its own — likewise;
/// - an icon in a `Pill` makes the pill need a `Tip`;
/// - every `Dot`, `Segments` and `Meter` carries a `Tip`, and a
///   `CheckStrip` carries one per check;
/// - no `Tip` is empty.
void main() {
  const widths = [390.0, 800.0, 1440.0];

  for (final screen in screens) {
    for (final MapEntry(key: name, value: build) in screen.cases.entries) {
      for (final width in widths) {
        testWidgets('${screen.name} ($name) at $width has every tooltip', (
          tester,
        ) async {
          tester.view.physicalSize = Size(width, 900);
          tester.view.devicePixelRatio = 1;
          addTearDown(tester.view.reset);
          await tester.pumpWidget(
            EmberApp(brightness: Brightness.light, home: build()),
          );
          final problems = untipped(tester);
          expect(problems, isEmpty, reason: problems.join('\n'));
        });
      }
    }
  }
}

/// Every place in the pumped tree that §9 says needs a tooltip and lacks
/// one, described for the failure message.
List<String> untipped(WidgetTester tester) {
  final problems = <String>[];

  for (final element in find.byType(Icon).evaluate()) {
    final icon = element.widget as Icon;
    if (icon.icon == null) continue;
    final what = 'icon ${_iconName(icon)}';

    final pill = _nearest<Pill>(element);
    if (pill != null) {
      // The chip's own tooltip: a Tip between the icon and the chip.
      final tip = _nearest<Tip>(element);
      final ownTip =
          tip != null &&
          (tip.widget as Tip).message.trim().isNotEmpty &&
          _isAncestor(pill, tip);
      if (!ownTip) {
        problems.add('$what in the chip "${(pill.widget as Pill).label}"');
      }
      continue;
    }

    final control = _nearestWhere(
      element,
      (e) =>
          e.widget is GestureDetector &&
          (e.widget as GestureDetector).onTap != null,
    );
    if (control != null) {
      if (_hasWords(control)) continue;
      if (_tipAbove(control) == null) {
        problems.add('$what: an icon-only control');
      }
      continue;
    }

    // Outside a control, words beside the icon in its row name it; an icon
    // on a line of its own (in a column, a wrap, or nothing) does not.
    final line = _nearestWhere(
      element,
      (e) => e.widget is Flex || e.widget is Wrap,
    );
    final inRow =
        line != null &&
        line.widget is Flex &&
        (line.widget as Flex).direction == Axis.horizontal;
    if (inRow && _hasWords(line)) continue;
    if (_tipAbove(element) == null) problems.add('$what stands alone');
  }

  for (final element in find.byType(Dot).evaluate()) {
    if (!_hasTipInside(element)) {
      problems.add('dot ${(element.widget as Dot).status.word}');
    }
  }
  for (final type in [Segments, Meter]) {
    for (final element in find.byType(type).evaluate()) {
      if (!_hasTipInside(element)) problems.add('$type has no tooltip');
    }
  }
  for (final element in find.byType(CheckStrip).evaluate()) {
    final checks = (element.widget as CheckStrip).checks.length;
    final tips = _countInside<Tip>(element);
    if (tips < checks) {
      problems.add('CheckStrip: $tips tooltips, $checks checks');
    }
  }

  for (final element in find.byType(Tip).evaluate()) {
    if ((element.widget as Tip).message.trim().isEmpty) {
      problems.add('an empty tooltip');
    }
  }
  return problems;
}

String _iconName(Icon icon) => '0x${icon.icon!.codePoint.toRadixString(16)}';

Element? _nearestWhere(Element from, bool Function(Element) test) {
  Element? found;
  from.visitAncestorElements((ancestor) {
    if (test(ancestor)) {
      found = ancestor;
      return false;
    }
    return true;
  });
  return found;
}

/// Whether [ancestor] is above [element].
bool _isAncestor(Element ancestor, Element element) =>
    _nearestWhere(element, (e) => e == ancestor) != null;

Element? _nearest<T extends Widget>(Element from) =>
    _nearestWhere(from, (e) => e.widget is T);

/// The nearest `Tip` above [from]. A `Tip` that also covers words does not
/// count: it is explaining something else.
Element? _tipAbove(Element from) {
  final tip = _nearest<Tip>(from);
  if (tip == null) return null;
  if ((tip.widget as Tip).message.trim().isEmpty) return null;
  if (_hasWords(tip)) return null;
  return tip;
}

bool _hasWords(Element root) {
  var found = false;
  void visit(Element e) {
    if (found) return;
    final w = e.widget;
    if ((w is Text && (w.data?.trim().isNotEmpty ?? true)) ||
        w is RichText && w.text.toPlainText().trim().isNotEmpty) {
      // An icon is drawn as a RichText of one glyph; that is not a word.
      if (w is RichText && _nearest<Icon>(e) != null) {
        e.visitChildren(visit);
        return;
      }
      found = true;
      return;
    }
    e.visitChildren(visit);
  }

  root.visitChildren(visit);
  return found;
}

bool _hasTipInside(Element root) => _countInside<Tip>(root) > 0;

int _countInside<T extends Widget>(Element root) {
  var count = 0;
  void visit(Element e) {
    if (e.widget is T) count++;
    e.visitChildren(visit);
  }

  root.visitChildren(visit);
  return count;
}
