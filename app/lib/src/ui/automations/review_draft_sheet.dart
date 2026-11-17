import '../../model/bots_model.dart';
import '../../model/fleet_store.dart' show describeFailure;
import '../kit/kit.dart';
import 'confirm_action.dart';

/// One held review, with its findings by severity and the four things a
/// person does with it: Post, Edit, Send to the author's agent, Discard
/// (F7 decision 4).
///
/// It is drawn inside the bot's own detail rather than in a second sheet
/// (design-system.md §5 keeps one sheet per screen), so [onClose] returns to
/// the bot.
class ReviewDraftView extends StatefulWidget {
  const ReviewDraftView({
    super.key,
    required this.model,
    required this.draft,
    required this.onClose,
  });

  final BotsModel model;
  final ReviewDraft draft;

  /// Goes back to the bot's own detail.
  final VoidCallback onClose;

  @override
  State<ReviewDraftView> createState() => _ReviewDraftViewState();
}

class _ReviewDraftViewState extends State<ReviewDraftView> {
  var _editing = false;
  var _confirming = false;
  var _posting = false;
  var _sending = false;
  String? _note;
  String? _error;

  late final _summary = TextEditingController(text: widget.draft.summary);
  late final _titles = {
    for (final finding in widget.draft.findings)
      finding.id: TextEditingController(text: finding.title),
  };

  @override
  void dispose() {
    _summary.dispose();
    for (final controller in _titles.values) {
      controller.dispose();
    }
    super.dispose();
  }

  Future<void> _post() async {
    setState(() {
      _posting = true;
      _note = null;
      _error = null;
    });
    try {
      final posted = await widget.model.postDraft(widget.draft);
      if (!mounted) return;
      setState(() {
        _posting = false;
        _note = posted.reviewURL == null || posted.reviewURL!.isEmpty
            ? 'Posted.'
            : 'Posted at ${posted.reviewURL}.';
      });
    } catch (error) {
      if (!mounted) return;
      setState(() {
        _posting = false;
        _error = describeFailure(error);
      });
    }
  }

  Future<void> _sendToAuthor() async {
    setState(() {
      _sending = true;
      _note = null;
      _error = null;
    });
    try {
      await widget.model.sendDraftToAuthor(widget.draft);
      if (!mounted) return;
      setState(() {
        _sending = false;
        _note = 'Sent to the agent that owns the pull request.';
      });
    } catch (error) {
      if (!mounted) return;
      setState(() {
        _sending = false;
        _error = describeFailure(error);
      });
    }
  }

  Future<void> _discard() async {
    setState(() => _error = null);
    try {
      await widget.model.discardDraft(widget.draft);
    } catch (error) {
      if (!mounted) return;
      setState(() => _error = describeFailure(error));
      return;
    }
    if (!mounted) return;
    widget.onClose();
  }

  Future<void> _saveEdit() async {
    setState(() {
      _note = null;
      _error = null;
    });
    try {
      await widget.model.editDraft(
        widget.draft,
        findings: [
          for (final finding in widget.draft.findings)
            Finding(
              id: finding.id,
              severity: finding.severity,
              path: finding.path,
              line: finding.line,
              title: _titles[finding.id]?.text ?? finding.title,
              body: finding.body,
              stepNode: finding.stepNode,
            ),
        ],
        summary: _summary.text,
      );
      if (!mounted) return;
      setState(() {
        _editing = false;
        _note = 'Saved. Nothing is posted until you post it.';
      });
    } catch (error) {
      if (!mounted) return;
      setState(() => _error = describeFailure(error));
    }
  }

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final draft = widget.draft;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Align(
          alignment: Alignment.centerLeft,
          child: Btn('Back to the bot', onTap: widget.onClose),
        ),
        const SizedBox(height: Space.s16),
        Text(draft.title ?? 'Pull request #${draft.number}', style: e.title),
        const SizedBox(height: Space.s4),
        Text(
          '${draft.repoID} #${draft.number} · ${draft.status} · '
          '${widget.model.ageLabel(draft.createdAt)} ago',
          style: e.small.tint(e.muted),
        ),
        const SizedBox(height: Space.s16),
        for (final severity in const ['blocking', 'should-fix', 'nit'])
          ..._findings(context, severity),
        if (draft.findings.isEmpty)
          Text('No findings: the review found nothing to flag.', style: e.body),
        const SizedBox(height: Space.s16),
        if (_editing)
          Field.multiline(label: 'Summary', controller: _summary)
        else ...[
          Text('Summary', style: e.small.tint(e.muted)),
          const SizedBox(height: Space.s4),
          Text(draft.summary, style: e.body),
        ],
        if (_note != null) ...[
          const SizedBox(height: Space.s12),
          Text(_note!, style: e.small.tint(e.muted)),
        ],
        if (_error != null) ...[
          const SizedBox(height: Space.s12),
          Text(_error!, style: e.small.tint(e.err)),
        ],
        const SizedBox(height: Space.s20),
        Wrap(
          spacing: Space.s8,
          runSpacing: Space.s8,
          children: [
            if (_editing)
              Btn('Save the edit', onTap: _saveEdit)
            else
              Btn.primary(
                _posting ? 'Posting' : 'Post',
                onTap: _posting ? null : _post,
              ),
            if (!_editing)
              Btn('Edit', onTap: () => setState(() => _editing = true)),
            Btn(
              _sending ? 'Sending' : 'Send to the author’s agent',
              onTap: _sending ? null : _sendToAuthor,
            ),
            if (!_editing && !_confirming)
              Btn('Discard', onTap: () => setState(() => _confirming = true)),
          ],
        ),
        if (_confirming) ...[
          const SizedBox(height: Space.s16),
          ConfirmAction(
            question: 'Discard this review of #${draft.number}?',
            loss:
                'The findings and the summary are deleted. The pull request '
                'itself is unchanged.',
            confirmLabel: 'Discard it',
            onConfirm: _discard,
            onCancel: () => setState(() => _confirming = false),
          ),
        ],
      ],
    );
  }

  /// One severity's findings, in the order the bridge listed them. A heading
  /// appears only when there is something under it.
  List<Widget> _findings(BuildContext context, String severity) {
    final e = Ember.of(context);
    final findings = [
      for (final finding in widget.draft.findings)
        if (finding.severity == severity) finding,
    ];
    if (findings.isEmpty) return const [];
    return [
      Label(_severityName(severity), count: findings.length),
      const SizedBox(height: Space.s8),
      for (final finding in findings) ...[
        if (_editing)
          Field(label: finding.title, controller: _titles[finding.id])
        else
          Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(finding.title, style: e.bodyStrong),
              if (finding.body != null && finding.body!.isNotEmpty) ...[
                const SizedBox(height: Space.s2),
                Text(finding.body!, style: e.small.tint(e.muted)),
              ],
              if (finding.path != null && finding.path!.isNotEmpty) ...[
                const SizedBox(height: Space.s2),
                Text(
                  finding.line == null
                      ? finding.path!
                      : '${finding.path}:${finding.line}',
                  style: e.monoSmall.tint(e.muted),
                ),
              ],
            ],
          ),
        const SizedBox(height: Space.s12),
      ],
    ];
  }

  /// What a severity is called where a person reads it.
  static String _severityName(String severity) => switch (severity) {
    'blocking' => 'Blocking',
    'should-fix' => 'Should fix',
    'nit' => 'Nits',
    _ => severity,
  };
}
