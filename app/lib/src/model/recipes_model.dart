import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import 'section_model.dart';

// A section's own type reaches its screens through the model, so `ui/` reads
// `model/` and never `client/` (test/boundary_test.dart).
export '../client/contract.g.dart' show Recipe, RecipeRun;

/// The Recipes section of Automations (F7 decision 4): the built-in recipes
/// and the user's own.
///
/// A built-in recipe cannot be edited or deleted — the bridge refuses both —
/// so the section offers copying it under a new name instead, and the model
/// never puts a built-in through a save.
class RecipesModel extends SectionModel<Recipe> {
  RecipesModel(this._bridge) : _fixture = null;

  /// A section with rows and no bridge, for the gallery and the screens the
  /// enforcement tests pump. A write on it says so rather than pretending.
  RecipesModel.fixture(List<Recipe> rows) : _bridge = null, _fixture = rows;

  final BridgeClient? _bridge;
  final List<Recipe>? _fixture;

  @override
  Future<List<Recipe>> fetch() async =>
      _fixture ?? await _requireBridge().recipes();

  @override
  Object keyOf(Recipe recipe) => recipe.name;

  @override
  bool isNew(Recipe recipe) => !items.any((held) => held.name == recipe.name);

  @override
  Future<Recipe> persist(Recipe recipe) {
    if (recipe.builtin ?? false) {
      throw StateError('a built-in recipe is copy only');
    }
    return _requireBridge().saveRecipe(recipe);
  }

  /// POST /api/recipes/{name}/copy: stores [recipe] under [name] and adds the
  /// copy to the list. The copy is the user's, so it can be edited.
  Future<Recipe> copy(Recipe recipe, String name) async {
    final copied = await _requireBridge().copyRecipe(recipe.name, name);
    store(copied);
    return copied;
  }

  /// DELETE /api/recipes/{name}, then drop the row.
  Future<void> delete(Recipe recipe) async {
    await _requireBridge().deleteRecipe(recipe.name);
    removed(recipe);
  }

  /// POST /api/recipes/{name}/run: starts it on [project], with the value of
  /// each placeholder [inputs] names.
  Future<void> run(
    Recipe recipe, {
    required String project,
    Map<String, String> inputs = const {},
  }) => _requireBridge().runRecipe(
    recipe.name,
    RecipeRun(project: project, repoID: '', ref: '', inputs: inputs),
  );

  BridgeClient _requireBridge() {
    final bridge = _bridge;
    if (bridge == null) {
      throw StateError('a fixture section has no bridge');
    }
    return bridge;
  }
}
