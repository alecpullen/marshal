import '../../model/section_model.dart';
import '../kit/kit.dart';
import 'detail_host.dart';

/// The one list-and-detail pattern of F7 (decision 2): a [PageBody], its
/// optional [Tabs], its [Rows], and the row's detail in a [Sheet] at 760 and
/// wider or a pushed page below.
///
/// It draws a [SectionModel] and nothing else: a section says how one row
/// reads and what its detail holds, and this does the rest — the save that
/// shows saving, the bridge's refusal in place, the reload a stale refusal
/// offers, and the states of §7.
class SectionPage<T> extends StatelessWidget {
  const SectionPage({
    super.key,
    required this.title,
    required this.model,
    required this.row,
    required this.detail,
    this.subtitle,
    this.actions = const [],
    this.tabs,
    this.tabIndex = 0,
    this.onTab,
    this.trailing,
    this.state,
    this.emptyMessage = 'Nothing here yet.',
    this.emptyAction,
    this.staleAge = 'a minute',
    this.unavailableHost = 'The daemon',
    this.unavailableSeen = 'a minute',
    this.detailTitle,
    this.createLabel,
    this.blank,
    this.readOnly = false,
    this.header = const [],
    this.footer = const [],
  });

  /// The page's title.
  final String title;

  /// The line under the title, when there is one.
  final String? subtitle;

  final SectionModel<T> model;

  /// What one row says. It must hand [onTap] to the row it builds, so
  /// choosing the row opens the detail.
  final RowItem Function(T item, VoidCallback onTap) row;

  /// The detail's body: the edit itself. Save and Cancel are added around
  /// it, and the bridge's refusal is drawn above it.
  final Widget Function(BuildContext context, T item) detail;

  /// The one primary action a section may add to its page header.
  final List<Widget> actions;

  /// The section's views of one object, when it has more than one.
  final List<TabItem>? tabs;
  final int tabIndex;
  final ValueChanged<int>? onTab;

  /// The place's own control, drawn at the end of the tab row when there are
  /// tabs (F9 B4): Automations' one New, beside the tabs whose kinds it can
  /// start.
  final Widget? trailing;

  /// A state the caller knows and the model cannot: the host is away, or
  /// the last read is old. Loading, empty, error and unavailable replace the
  /// rows; stale keeps them and marks them (§7).
  final StateKind? state;

  final String emptyMessage;
  final StateAction? emptyAction;
  final String staleAge;
  final String unavailableHost;
  final String unavailableSeen;

  /// The detail's title, when it is not the row's name — a new item has
  /// none yet.
  final String Function(T item)? detailTitle;

  /// The section's one primary action in its page header: a "New …" button
  /// that opens [blank]'s detail, where a save creates it. Both or neither.
  final String? createLabel;
  final T Function()? blank;

  /// A detail with nothing to save — a watch, a read-only view: it draws a
  /// Close instead of Save and Cancel.
  final bool readOnly;

  /// What the section draws above its rows — a suggestion, an offer — and
  /// below them. Both are the section's own content, never a second list
  /// pattern.
  final List<Widget> header;
  final List<Widget> footer;

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: model,
      builder: (context, _) {
        final tabs = this.tabs;
        final create = createLabel;
        final blank = this.blank;
        return PageBody(
          title: title,
          subtitle: subtitle,
          actions: [
            if (create != null && blank != null)
              Btn.primary(create, onTap: () => _open(context, blank())),
            ...actions,
          ],
          width: PageWidth.list,
          children: [
            if (tabs != null) ...[
              Row(
                children: [
                  Expanded(
                    child: Tabs(
                      tabs: tabs,
                      index: tabIndex,
                      onChanged: onTab ?? (_) {},
                    ),
                  ),
                  ?trailing,
                ],
              ),
              const SizedBox(height: Space.s20),
            ],
            ...header,
            if (header.isNotEmpty) const SizedBox(height: Space.s20),
            ..._content(context),
            if (footer.isNotEmpty) ...[
              const SizedBox(height: Space.s20),
              ...footer,
            ],
          ],
        );
      },
    );
  }

  /// The rows, or the state that stands in for them.
  List<Widget> _content(BuildContext context) {
    final retry = StateAction('Read the section again', onTap: model.load);
    final empty = StateAction(
      emptyAction?.label ?? 'Read the section again',
      onTap: emptyAction?.onTap ?? model.load,
    );
    switch (state) {
      case StateKind.loading:
        return const [StateMessage.loading(lines: 5)];
      case StateKind.empty:
        return [StateMessage.empty(message: emptyMessage, action: empty)];
      case StateKind.error:
        return [
          StateMessage.error(
            message: model.error ?? 'The section could not be read.',
            retry: retry,
          ),
        ];
      case StateKind.unauthorized:
        return const [StateMessage.unauthorized()];
      case StateKind.unavailable:
        return [
          StateMessage.unavailable(
            host: unavailableHost,
            lastSeen: unavailableSeen,
            action: retry,
          ),
        ];
      case StateKind.planned:
        return [StateMessage.planned(name: title, note: 'Not built yet')];
      case StateKind.stale:
        // The last data is still shown, plainly marked with its age.
        final marked = StateMessage.stale(age: staleAge);
        if (model.items.isEmpty) return [marked];
        return [marked, const SizedBox(height: Space.s16), _rows(context)];
      case null:
        break;
    }

    if (model.items.isEmpty) {
      if (model.loading) return const [StateMessage.loading(lines: 5)];
      final error = model.error;
      if (error != null) {
        return [StateMessage.error(message: error, retry: retry)];
      }
      return [StateMessage.empty(message: emptyMessage, action: empty)];
    }
    return [_rows(context)];
  }

  Widget _rows(BuildContext context) => Rows([
    for (final item in model.items) row(item, () => _open(context, item)),
  ]);

  /// Opens the detail the way every section does (F7 decision 2).
  void _open(BuildContext context, T item) {
    model.edit(item);
    openDetail(
      context,
      title: detailTitle?.call(item) ?? row(item, () {}).name,
      body: _Detail<T>(model: model, detail: detail),
      actions: [_DetailActions<T>(model: model, readOnly: readOnly)],
    );
  }
}

/// A detail's content: the refusal above, then the edit. Both listen to the
/// model, so a save's state shows without reopening anything.
class _Detail<T> extends StatelessWidget {
  const _Detail({required this.model, required this.detail});

  final SectionModel<T> model;
  final Widget Function(BuildContext context, T item) detail;

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: model,
      builder: (context, _) {
        final item = model.editing;
        final refusal = model.saveError;
        return Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            if (refusal != null) ...[
              _Refusal(refusal, stale: model.saveState == SaveState.stale),
              const SizedBox(height: Space.s16),
            ],
            if (item != null) detail(context, item),
          ],
        );
      },
    );
  }
}

/// The bridge's refusal, in its own words, where the edit is (F7 decision 3).
/// A stale refusal says what a reload is for; the reload itself is an action
/// of the detail.
class _Refusal extends StatelessWidget {
  const _Refusal(this.message, {required this.stale});

  final String message;
  final bool stale;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Box(
      tone: BoxTone.failing,
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Padding(
            padding: const EdgeInsets.only(top: Space.s2),
            child: Icon(
              Glyphs.exclamationmark_circle,
              size: Sizes.iconRow,
              color: e.err,
            ),
          ),
          const SizedBox(width: Space.s8),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(message, style: e.body),
                if (stale) ...[
                  const SizedBox(height: Space.s4),
                  Text(
                    'Something else changed it while you were editing. '
                    'Reload to see what it holds now, then save again.',
                    style: e.small.tint(e.muted),
                  ),
                ],
              ],
            ),
          ),
        ],
      ),
    );
  }
}

/// A detail's Save, Cancel and — after a stale refusal — Reload. Save is the
/// one primary action and it comes first; while a save is in flight both
/// Save and Cancel are disabled, because the bridge has not answered yet.
class _DetailActions<T> extends StatelessWidget {
  const _DetailActions({required this.model, required this.readOnly});

  final SectionModel<T> model;
  final bool readOnly;

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: model,
      builder: (context, _) {
        if (readOnly) {
          return Wrap(
            spacing: Space.s8,
            runSpacing: Space.s8,
            children: [Btn('Close', onTap: () => _close(context))],
          );
        }
        final state = model.saveState;
        final saving = state == SaveState.saving;
        return Wrap(
          spacing: Space.s8,
          runSpacing: Space.s8,
          children: [
            Btn.primary(
              saving ? 'Saving' : 'Save',
              onTap: saving || !model.hasUnsavedEdits
                  ? null
                  : () => _save(context),
            ),
            Btn('Cancel', onTap: saving ? null : () => _close(context)),
            if (state == SaveState.stale)
              Btn('Reload', onTap: () => model.reloadAfterStale()),
          ],
        );
      },
    );
  }

  Future<void> _save(BuildContext context) async {
    await model.save();
    // A refusal keeps the detail open, with the bridge's words in place.
    if (model.saveState != SaveState.clean) return;
    if (!context.mounted) return;
    _close(context);
  }

  void _close(BuildContext context) {
    model.cancel();
    Navigator.of(context).maybePop();
  }
}
