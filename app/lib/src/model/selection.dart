// [ProjectMemory]'s fields stay private, so the functions it is built from
// cannot be initializing formals: Dart has no private named parameter.
// ignore_for_file: prefer_initializing_formals

import 'package:flutter/foundation.dart';

import 'stack_store.dart';

/// One end of a selection: a row's node, and a character offset into that
/// node's plain text.
///
/// It is model-backed (findings/transcript-selection.md): the anchor names a
/// node, not a widget, so a selection survives rows scrolling out of the
/// build window and rows arriving while it is made.
@immutable
class TextAnchor {
  const TextAnchor(this.nodeId, this.offset);

  /// The node whose row the anchor is in.
  final String nodeId;

  /// The character offset into that node's plain text.
  final int offset;

  @override
  bool operator ==(Object other) =>
      other is TextAnchor && other.nodeId == nodeId && other.offset == offset;

  @override
  int get hashCode => Object.hash(nodeId, offset);

  @override
  String toString() => 'TextAnchor($nodeId, $offset)';
}

/// One transcript's selection: two anchors, in the order the user made them.
///
/// It holds no widgets and no text: the text comes from the store when the
/// selection is copied, and [textOf] answers for the word and line helpers.
/// That is what lets a selection cross rows the list has not built and stay
/// put while the agent appends.
class TranscriptSelection extends ChangeNotifier {
  TranscriptSelection({required this.textOf});

  /// The plain text of one node, the same projection selection copies from.
  final String Function(String nodeId) textOf;

  TextAnchor? _base;
  TextAnchor? _extent;

  /// Where the selection started.
  TextAnchor? get base => _base;

  /// Where it was last extended to.
  TextAnchor? get extent => _extent;

  /// Whether anything is selected.
  bool get isEmpty => _base == null && _extent == null;

  /// Starts a selection at [at].
  void begin(TextAnchor at) {
    _base = at;
    _extent = at;
    notifyListeners();
  }

  /// Moves the free end to [to], keeping where the selection started.
  void extend(TextAnchor to) {
    if (_base == null) {
      begin(to);
      return;
    }
    if (_extent == to) return;
    _extent = to;
    notifyListeners();
  }

  /// Drops the selection.
  void clear() {
    if (_base == null && _extent == null) return;
    _base = null;
    _extent = null;
    notifyListeners();
  }

  /// Selects the word [at] falls in, the way a double click does. A click on
  /// the space between words selects the word after it.
  void selectWord(TextAnchor at) {
    final text = textOf(at.nodeId);
    var offset = at.offset.clamp(0, text.length);
    // A click on whitespace takes the word after it, as a browser does, rather
    // than running back into the word before it.
    while (offset < text.length && _isSpace(text[offset])) {
      offset++;
    }
    var start = offset;
    var end = offset;
    while (start > 0 && !_isSpace(text[start - 1])) {
      start--;
    }
    while (end < text.length && !_isSpace(text[end])) {
      end++;
    }
    _base = TextAnchor(at.nodeId, start);
    _extent = TextAnchor(at.nodeId, end);
    notifyListeners();
  }

  /// Selects the line [at] falls in, the way a triple click does.
  void selectLine(TextAnchor at) {
    final text = textOf(at.nodeId);
    final offset = at.offset.clamp(0, text.length);
    var start = offset;
    var end = offset;
    while (start > 0 && text[start - 1] != '\n') {
      start--;
    }
    while (end < text.length && text[end] != '\n') {
      end++;
    }
    _base = TextAnchor(at.nodeId, start);
    _extent = TextAnchor(at.nodeId, end);
    notifyListeners();
  }

  /// Selects every row in [order], from the first character of the first to
  /// the last of the last.
  void selectAll(List<String> order) {
    if (order.isEmpty) {
      clear();
      return;
    }
    final last = order.last;
    _base = TextAnchor(order.first, 0);
    _extent = TextAnchor(last, textOf(last).length);
    notifyListeners();
  }

  /// The two anchors, the earlier in [order] first, or null when nothing is
  /// selected or an end is not in the list.
  (TextAnchor, TextAnchor)? range(List<String> order) {
    final base = _base;
    final extent = _extent;
    if (base == null || extent == null) return null;
    final a = order.indexOf(base.nodeId);
    final b = order.indexOf(extent.nodeId);
    if (a < 0 || b < 0) return null;
    return a <= b ? (base, extent) : (extent, base);
  }

  /// The selected text, in [order], one line per row: what copying puts on
  /// the clipboard.
  ///
  /// Every row between the two anchors contributes, whether or not the list
  /// built it, because the text comes from the store rather than the widgets.
  String copy(StackStore store, List<String> order) {
    final r = range(order);
    if (r == null) return '';
    final (a, b) = r;
    final from = order.indexOf(a.nodeId);
    final to = order.indexOf(b.nodeId);
    final out = <String>[];
    for (var i = from; i <= to; i++) {
      final text = store.plainText(order[i]);
      final start = i == from ? a.offset.clamp(0, text.length) : 0;
      final end = i == to ? b.offset.clamp(0, text.length) : text.length;
      if (end < start) continue;
      out.add(text.substring(start, end));
    }
    return out.join('\n');
  }

  /// The part of [nodeId]'s text the selection covers, or null when it covers
  /// none of it. A row between the anchors is covered whole.
  (int, int)? spanFor(String nodeId, List<String> order, String text) {
    final r = range(order);
    if (r == null) return null;
    final (a, b) = r;
    final index = order.indexOf(nodeId);
    final from = order.indexOf(a.nodeId);
    final to = order.indexOf(b.nodeId);
    if (index < from || index > to) return null;
    final start = index == from ? a.offset.clamp(0, text.length) : 0;
    final end = index == to ? b.offset.clamp(0, text.length) : text.length;
    if (end <= start) return null;
    return (start, end);
  }

  static bool _isSpace(String ch) => ch == ' ' || ch == '\n' || ch == '\t';
}

/// What the Studio remembers about where a person was working: the project
/// they selected last, and the last thread they read in each project on each
/// host (agent-first Studio, task 2).
///
/// It is kept on the device that chose it through the Studio's own preference
/// store — the seam `GeneralPreferences` uses for appearance and density — and
/// [read]/[save] are handed in rather than a store, so `model/` reaches no
/// client and a test keeps its memory in a map.
///
/// A thread is remembered per host and project because a name is not an
/// identity: two hosts may serve a project with the same directory name, and
/// restoring the wrong one's thread would answer for work the person never
/// had open. Only a *usable* thread is remembered — one the fleet still lists
/// and that is not closed — so a stale entry opens a focused draft instead of
/// a conversation that no longer exists.
class ProjectMemory {
  ProjectMemory({
    String? Function(String key)? read,
    void Function(String key, String value)? save,
  }) : _read = read,
       _save = save;

  /// The preference key the project selected last is kept under: its host and
  /// its root, each percent-encoded so one cannot be read as the other.
  static const projectKey = 'project.last';

  /// The preference key the thread last read in [project] on [host] is kept
  /// under. Percent-encoded for the same reason as [projectKey].
  static String threadKey(String host, String project) =>
      'project.thread.${Uri.encodeComponent(host)}.'
      '${Uri.encodeComponent(project)}';

  final String? Function(String key)? _read;
  final void Function(String key, String value)? _save;

  /// The project selected last, or null when none was — or when what was kept
  /// cannot be read back.
  ({String host, String project})? get lastProject {
    final kept = _read?.call(projectKey);
    if (kept == null || kept.isEmpty) return null;
    final cut = kept.indexOf('/');
    if (cut <= 0 || cut == kept.length - 1) return null;
    try {
      return (
        host: Uri.decodeComponent(kept.substring(0, cut)),
        project: Uri.decodeComponent(kept.substring(cut + 1)),
      );
    } on ArgumentError {
      // A malformed escape names no project; the Studio opens as it would on
      // a first visit rather than failing.
      return null;
    }
  }

  /// Remembers [project] on [host] as the project selected last.
  void rememberProject(String host, String project) => _save?.call(
    projectKey,
    '${Uri.encodeComponent(host)}/${Uri.encodeComponent(project)}',
  );

  /// The thread [host]'s [project] last had open, or null when it has none.
  String? threadFor(String host, String project) {
    final id = _read?.call(threadKey(host, project));
    return id == null || id.isEmpty ? null : id;
  }

  /// Remembers [thread] as the thread read last in [project] on [host].
  void rememberThread(String host, String project, String thread) =>
      _save?.call(threadKey(host, project), thread);

  /// Drops the thread remembered for [project] on [host]: it closed, or no
  /// host lists it any more, so it is not what selecting the project resumes.
  void forgetThread(String host, String project) =>
      _save?.call(threadKey(host, project), '');
}
