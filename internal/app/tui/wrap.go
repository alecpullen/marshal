package tui

// WrapBreakpoints are the characters ansi.Wrap may break a long token at.
//
// Every wrap site previously passed "", which breaks tokens at whatever
// character lands on the limit: "fieldlist.go:" / "624", or ".../seg" /
// "ment?x=1". A coding agent's transcript is mostly file references and
// URLs, and a split one stops being a copyable, clickable unit in every
// terminal that detects them.
//
// Breaking at "/" fixes paths and URLs; "-" and ":" extend the same
// treatment to flags (--max-tokens) and file:line references. Measured at
// width 40, this also packs more content per line than "" does, because the
// wrapper stops stranding a whole token on its own line.
const WrapBreakpoints = "/-:"
