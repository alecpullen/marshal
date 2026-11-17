import 'package:flutter/widgets.dart';
import 'package:markdown/markdown.dart' as md;

import '../tokens.dart';
import 'hairline.dart';
import 'output_block.dart';

/// What the agent said, in prose (design-system.md §5).
///
/// The text is Markdown. The `markdown` package parses it into nodes and
/// nothing else: every node is drawn here with a kit text role, so no
/// third-party widget sets a size, a weight or a colour. Only the constructs
/// the agent actually writes are drawn — paragraphs, headings, lists,
/// quotes, rules and code — and anything else is shown as its plain text.
class Narration extends StatelessWidget {
  const Narration({super.key, required this.text});

  /// The narration, as Markdown.
  final String text;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final blocks = md.Document(
      extensionSet: md.ExtensionSet.gitHubWeb,
      encodeHtml: false,
    ).parse(text);
    if (blocks.isEmpty) {
      return Padding(
        padding: const EdgeInsets.only(bottom: Space.s12),
        child: Text(text, style: e.reading),
      );
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        for (final block in blocks)
          Padding(
            padding: const EdgeInsets.only(bottom: Space.s12),
            child: _block(context, block),
          ),
      ],
    );
  }

  Widget _block(BuildContext context, md.Node node) {
    final e = Ember.of(context);
    if (node is md.Text) return Text(node.text, style: e.reading);
    if (node is! md.Element) return const SizedBox.shrink();
    switch (node.tag) {
      case 'p':
        return Text.rich(TextSpan(children: _inline(context, node.children)));
      case 'h1':
      case 'h2':
      case 'h3':
      case 'h4':
      case 'h5':
      case 'h6':
        return Text.rich(
          TextSpan(children: _inline(context, node.children)),
          style: e.bodyStrong,
        );
      case 'pre':
        final code = node.children?.whereType<md.Element>().firstOrNull;
        return OutputBlock(text: (code ?? node).textContent);
      case 'blockquote':
        return DecoratedBox(
          decoration: BoxDecoration(
            border: Border(
              left: BorderSide(color: e.border, width: Sizes.border),
            ),
          ),
          child: Padding(
            padding: const EdgeInsets.only(left: Space.s12),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                for (final child in node.children ?? const [])
                  _block(context, child),
              ],
            ),
          ),
        );
      case 'ul':
      case 'ol':
        final items =
            node.children?.whereType<md.Element>().toList() ?? const [];
        return Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            for (final (index, item) in items.indexed)
              Padding(
                padding: const EdgeInsets.only(bottom: Space.s2),
                child: Row(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    SizedBox(
                      width: Space.s20,
                      child: Text(
                        node.tag == 'ol' ? '${index + 1}.' : '•',
                        style: e.body.tint(e.muted),
                      ),
                    ),
                    Expanded(
                      child: Text.rich(
                        TextSpan(children: _inline(context, item.children)),
                      ),
                    ),
                  ],
                ),
              ),
          ],
        );
      case 'hr':
        return const Hairline();
      default:
        return Text(node.textContent, style: e.reading);
    }
  }

  /// The spans of one inline run, each role taken from the tokens.
  List<InlineSpan> _inline(BuildContext context, List<md.Node>? nodes) {
    final e = Ember.of(context);
    final spans = <InlineSpan>[];
    for (final node in nodes ?? const <md.Node>[]) {
      if (node is md.Text) {
        spans.add(TextSpan(text: node.text, style: e.reading));
        continue;
      }
      if (node is! md.Element) continue;
      switch (node.tag) {
        case 'code':
          spans.add(TextSpan(text: node.textContent, style: e.mono));
        case 'strong':
          spans.add(TextSpan(text: node.textContent, style: e.bodyStrong));
        case 'a':
          // The link's words are what a reader sees; the address is not drawn
          // here because a transcript's links are followed from the inspector.
          spans.add(TextSpan(text: node.textContent, style: e.reading));
        case 'br':
          spans.add(const TextSpan(text: '\n'));
        default:
          spans.addAll(_inline(context, node.children));
      }
    }
    return spans;
  }
}
