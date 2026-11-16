import 'package:flutter/services.dart';
import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'press.dart';

/// One choice in a [Menu]: its name and one line saying what it does.
@immutable
class MenuChoice {
  const MenuChoice(this.name, this.explanation, {required this.onSelect});

  final String name;
  final String explanation;
  final VoidCallback onSelect;
}

/// A popover of choices (design-system.md §5). Each item is a name and one
/// line of explanation. It opens under its anchor and closes on a choice,
/// on Escape, or on a click outside it.
///
/// [anchor] draws the control that opens it — an `IconBtn` caret or a
/// `Btn` — given the callback that opens and closes the menu.
class Menu extends StatefulWidget {
  const Menu({super.key, required this.choices, required this.anchor});

  final List<MenuChoice> choices;
  final Widget Function(BuildContext context, VoidCallback toggle) anchor;

  /// The popover's width, which its explanations wrap within.
  static const double width = Layout.sidebar;

  @override
  State<Menu> createState() => _MenuState();
}

class _MenuState extends State<Menu> {
  final _controller = MenuController();

  /// The open popover's focus, so the keyboard reaches its choices and
  /// Escape reaches the popover rather than whatever opened it.
  final _panel = FocusScopeNode(debugLabel: 'Menu');

  void _toggle() =>
      _controller.isOpen ? _controller.close() : _controller.open();

  @override
  void dispose() {
    _panel.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return RawMenuAnchor(
      controller: _controller,
      consumeOutsideTaps: true,
      onOpen: () => WidgetsBinding.instance.addPostFrameCallback(
        (_) => _panel.requestFocus(),
      ),
      overlayBuilder: (context, info) {
        final e = Ember.of(context);
        final below = info.anchorRect.bottomLeft + const Offset(0, Space.s4);
        final left = below.dx.clamp(
          0.0,
          (info.overlaySize.width - Menu.width).clamp(0.0, double.infinity),
        );
        return Positioned(
          left: left,
          top: below.dy,
          width: Menu.width,
          child: TapRegion(
            groupId: info.tapRegionGroupId,
            // The anchor's own region closes only submenus on an outside
            // tap; the popover closes itself.
            onTapOutside: (_) => _controller.close(),
            child: Shortcuts(
              shortcuts: const {
                SingleActivator(LogicalKeyboardKey.escape): DismissIntent(),
              },
              child: Actions(
                actions: {
                  DismissIntent: CallbackAction<DismissIntent>(
                    onInvoke: (_) {
                      _controller.close();
                      return null;
                    },
                  ),
                },
                child: FocusScope(
                  node: _panel,
                  child: DecoratedBox(
                    decoration: BoxDecoration(
                      color: e.bg,
                      border: Border.all(color: e.border, width: Sizes.border),
                      borderRadius: BorderRadius.circular(Radii.panel),
                    ),
                    child: Padding(
                      padding: const EdgeInsets.all(Space.s4),
                      child: Column(
                        mainAxisSize: MainAxisSize.min,
                        crossAxisAlignment: CrossAxisAlignment.stretch,
                        children: [
                          for (final choice in widget.choices)
                            _Item(
                              choice,
                              onSelect: () {
                                _controller.close();
                                choice.onSelect();
                              },
                            ),
                        ],
                      ),
                    ),
                  ),
                ),
              ),
            ),
          ),
        );
      },
      builder: (context, controller, child) => widget.anchor(context, _toggle),
    );
  }
}

class _Item extends StatelessWidget {
  const _Item(this.choice, {required this.onSelect});

  final MenuChoice choice;
  final VoidCallback onSelect;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Press(
      onTap: onSelect,
      ringInside: true,
      builder: (context, state) => Container(
        padding: const EdgeInsets.symmetric(
          horizontal: Space.s8,
          vertical: Space.s6,
        ),
        decoration: BoxDecoration(
          color: state.hovered ? e.hover : null,
          borderRadius: BorderRadius.circular(Radii.control),
        ),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(choice.name, style: e.body),
            Text(choice.explanation, style: e.small.tint(e.muted)),
          ],
        ),
      ),
    );
  }
}
