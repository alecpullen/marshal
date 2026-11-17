/// The stub is what a platform with no page sees: a native build has no URL
/// fragment to redeem, and `marshal studio` opens a browser, not a window.
Future<void> adoptStudioCodeFromPage() async {}
