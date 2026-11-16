import '../../model/environment_model.dart';
import '../kit/kit.dart';

/// The designer's layer list (F7 decision 6): one row per layer, the chosen
/// one filled, saying what the layer holds and the lines it owns.
///
/// Choosing a row is the same choice the source view makes: the model selects
/// the layer's range in the source.
class LayerList extends StatelessWidget {
  const LayerList({super.key, required this.model});

  final EnvironmentModel model;

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: model,
      builder: (context, _) {
        final e = Ember.of(context);
        if (model.layers.isEmpty) {
          return Text(
            'No layer yet: a workspace starts with [workspace].',
            style: e.body.tint(e.muted),
          );
        }
        return Rows([
          for (final layer in model.layers)
            RowItem(
              name: layer.kind.label,
              description: layer.summary,
              value: 'lines ${layer.rangeLabel}',
              selected: model.selected == layer.kind,
              onTap: () => model.select(layer.kind),
            ),
        ]);
      },
    );
  }
}
