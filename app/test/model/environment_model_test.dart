import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/environment_model.dart';

/// The designer's scan (F7 decision 6): the source is the authority, and the
/// layers are read from it — one layer per table the design names, however
/// many blocks the table is written in.
///
/// TOML declares a table more than once with `[[name]]`, and a scan that kept
/// only each name's first header gave the block to whichever layer happened to
/// end last, so editing that layer deleted it.
void main() {
  const source = '''[workspace]
name = "go-service"

[[mounts]]
path = "/src"

[network]
mode = "allowlist"

[[mounts]]
path = "/cache"
''';

  EnvironmentModel model() =>
      EnvironmentModel.fixture(name: 'go-service', source: source);

  test('a table written twice is one layer holding both blocks', () {
    final m = model();
    addTearDown(m.dispose);

    expect(m.textOf(LayerKind.mounts), contains('path = "/src"'));
    expect(m.textOf(LayerKind.mounts), contains('path = "/cache"'));
  });

  test('a block between two of a table belongs to no other layer', () {
    final m = model();
    addTearDown(m.dispose);

    expect(m.textOf(LayerKind.network), isNot(contains('[[mounts]]')));
    expect(m.textOf(LayerKind.network), contains('mode = "allowlist"'));
  });

  test('editing a table before a repeated block keeps the block', () {
    final m = model();
    addTearDown(m.dispose);

    m.editLayer(LayerKind.network, '[network]\nmode = "open"');

    expect(m.source, contains('mode = "open"'));
    expect(m.source, contains('path = "/src"'));
    expect(m.source, contains('path = "/cache"'));
    expect(m.source, contains('[workspace]'));
  });

  test('editing a repeated table writes its blocks back as one', () {
    final m = model();
    addTearDown(m.dispose);

    m.editLayer(
      LayerKind.mounts,
      '[[mounts]]\npath = "/src"\n\n[[mounts]]\npath = "/tmp"',
    );

    expect(m.source, contains('path = "/tmp"'));
    expect(m.source, isNot(contains('path = "/cache"')));
    expect(m.source, contains('[network]\nmode = "allowlist"'));
    expect(
      RegExp('\n\n\n').hasMatch(m.source),
      isFalse,
      reason: 'the block that went leaves one blank line, not two',
    );
  });
}
