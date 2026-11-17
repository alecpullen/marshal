import '../kit/kit.dart';

/// Where a section's detail opens (F7 decision 2, plan decision "a detail
/// opens in a [Sheet] at 760 and wider and as a pushed page below").
///
/// One place decides it, so every section opens the same way at the same
/// width. The detail's own actions are the caller's: at most one primary,
/// and it comes first (design-system.md §5).
Future<void> openDetail(
  BuildContext context, {
  required String title,
  required Widget body,
  List<Widget> actions = const [],
}) {
  final wide = MediaQuery.sizeOf(context).width >= Layout.narrow;
  if (wide) {
    return Sheet.show<void>(
      context,
      Sheet(title: title, actions: actions, child: body),
    );
  }
  return Navigator.of(context)
      .push<void>(_DetailRoute(title: title, actions: actions, body: body));
}

/// The narrow layout's detail: the same title, body and actions at the top
/// of a page of its own, so the detail is one tap from the list on a window
/// with no room for a sheet beside it.
class _DetailPage extends StatelessWidget {
  const _DetailPage({
    required this.title,
    required this.body,
    required this.actions,
  });

  final String title;
  final Widget body;
  final List<Widget> actions;

  @override
  Widget build(BuildContext context) {
    return PageBody(
      title: title,
      width: PageWidth.list,
      actions: actions,
      children: [body],
    );
  }
}

/// The pushed page: nothing animates (design-system.md §9).
class _DetailRoute extends PageRouteBuilder<void> {
  _DetailRoute({
    required String title,
    required Widget body,
    required List<Widget> actions,
  }) : super(
         transitionDuration: Duration.zero,
         reverseTransitionDuration: Duration.zero,
         pageBuilder: (context, _, _) =>
             _DetailPage(title: title, body: body, actions: actions),
       );
}
