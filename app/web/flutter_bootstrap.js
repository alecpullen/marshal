{{flutter_js}}
{{flutter_build_config}}

// The engine fetches a fallback font when a glyph is in none of the bundled
// fonts; by default it asks fonts.gstatic.com. Pointing it at this app's own
// assets keeps every request on the serving host (f0-foundations.md, item 7):
// a glyph no bundled font has is drawn as missing instead. Build with
// --no-web-resources-cdn so the engine itself is served locally too.
_flutter.loader.load({
  config: {
    fontFallbackBaseUrl: "assets/fonts/fallback/",
  },
});
