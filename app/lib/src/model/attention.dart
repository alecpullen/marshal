import 'nav_model.dart' show projectName;

/// Where an attention entry came from.
///
/// The Inbox merges several feeds into one queue. Each entry says which feed
/// reported it, so a factory draft, a review bot's held draft and an agent's
/// own question are never read as one source speaking for another.
enum AskSource {
  /// The fleet's own stream: an agent's parked request, an intake submission,
  /// a pull request whose fix failed.
  fleet,

  /// The review bot's held drafts, handed over by the Bots section.
  review,

  /// The egress proxy's blocked requests, handed over by the Network section.
  network,

  /// A work item the factory owns, read over its own route.
  factory,
}

extension AskSourceWords on AskSource {
  /// The source's name in a card's source line.
  String get label => switch (this) {
    AskSource.fleet => 'Fleet',
    AskSource.review => 'Review bot',
    AskSource.network => 'Network',
    AskSource.factory => 'Factory',
  };

  /// What this source's reports cover, for a tooltip that says more than the
  /// label.
  String get note => switch (this) {
    AskSource.fleet =>
      "Reported by the daemon's own fleet stream, so it goes when the daemon "
          'stops reporting it.',
    AskSource.review =>
      'Reported by the review bot, which holds the draft until a person '
          'posts, sends or discards it.',
    AskSource.network =>
      'Reported by the egress proxy, which holds the request until a person '
          'decides it.',
    AskSource.factory =>
      "Read from the factory's work items, which are polled apart from the "
          'fleet stream.',
  };
}

/// Who an entry belongs to.
///
/// There is no `mine` value: the bridge has no viewer identity to compare a
/// record's `ownerId` with (no `/api/me` route, and
/// `web/bridge/contract/intake.go` records that today's bridge never sets an
/// intake owner), so an entry cannot honestly claim to be this person's.
enum AskScope {
  /// A source the daemon reports for the whole bridge.
  everyone,

  /// A source whose owner the contract does not name: the entry is stated as
  /// the project's rather than guessing a person.
  projectWide,
}

extension AskScopeWords on AskScope {
  /// The scope's name in a card's source line.
  String get label => switch (this) {
    AskScope.everyone => 'Everyone',
    AskScope.projectWide => 'Project-wide',
  };

  /// What the scope means for the person, for a tooltip.
  String get note => switch (this) {
    AskScope.everyone =>
      'The daemon reports this for the whole bridge, not for one person.',
    AskScope.projectWide =>
      'The factory record names no owner, so this is stated as the project’s '
          'rather than guessing who it belongs to.',
  };
}

/// Where an ask came from, as a card's source line: its source, its project
/// when the source names one, and its scope.
///
/// The project is omitted rather than guessed when the source does not name
/// one (an intake submission and a review draft carry a repository id, not a
/// project root). [projectName] is the same helper the Overview's project
/// table uses.
String provenanceOf(AskSource source, String project, AskScope scope) => [
  source.label,
  if (project.isNotEmpty) projectName(project),
  scope.label,
].join(' · ');
