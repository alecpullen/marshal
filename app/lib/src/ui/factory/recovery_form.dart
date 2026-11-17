import '../kit/kit.dart';

/// What a person chose to do about an item that stalled or ran out of rounds
/// (F6 decision 5).
class RecoveryChoice {
  const RecoveryChoice({this.model = '', this.fresh = false, this.rounds = 0});

  /// The model to continue on, or empty to leave the routing alone.
  final String model;

  /// Start on a fresh context rather than continuing the one that stalled.
  final bool fresh;

  /// How many more rounds to allow.
  final int rounds;

  @override
  String toString() =>
      'RecoveryChoice(${model.isEmpty ? 'same model' : model}, '
      '${fresh ? 'fresh' : 'continue'}, +$rounds)';
}

/// A recovery's settings, inline in its ask card: the model to continue on,
/// whether to continue the context or start fresh, and how many more rounds to
/// allow.
///
/// The card carries them because recovery is a decision about *how* the work
/// goes on, and a person who has to open another screen to make it has lost
/// the diagnosis they were reading.
class RecoveryForm extends StatefulWidget {
  const RecoveryForm({super.key, required this.onSubmit, this.enabled = true});

  /// Sends the choice. Null-safe: the form's submit is disabled when the host
  /// gave no handler.
  final void Function(RecoveryChoice choice)? onSubmit;

  /// Whether the form can be used now: false while its command is in flight.
  final bool enabled;

  @override
  State<RecoveryForm> createState() => _RecoveryFormState();
}

class _RecoveryFormState extends State<RecoveryForm> {
  final _model = TextEditingController();
  final _rounds = TextEditingController(text: '1');
  bool _fresh = false;

  static const _continueLabel = 'Continue this context';
  static const _freshLabel = 'Start fresh';

  @override
  void dispose() {
    _model.dispose();
    _rounds.dispose();
    super.dispose();
  }

  /// One of the two context choices: the selected one is the primary button.
  Widget _context(
    String label, {
    required bool selected,
    required VoidCallback onTap,
  }) {
    final tap = widget.enabled ? onTap : null;
    return selected ? Btn.primary(label, onTap: tap) : Btn(label, onTap: tap);
  }

  @override
  Widget build(BuildContext context) {
    final submit = widget.onSubmit;
    final rounds = int.tryParse(_rounds.text.trim()) ?? 0;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Field(
          label: 'Model',
          controller: _model,
          hint: 'Leave it empty to keep the one that stalled',
        ),
        const SizedBox(height: Space.s12),
        Row(
          children: [
            _context(
              _continueLabel,
              selected: !_fresh,
              onTap: () => setState(() => _fresh = false),
            ),
            const SizedBox(width: Space.s8),
            _context(
              _freshLabel,
              selected: _fresh,
              onTap: () => setState(() => _fresh = true),
            ),
          ],
        ),
        const SizedBox(height: Space.s12),
        Field(label: 'Extra rounds', controller: _rounds, hint: '1'),
        const SizedBox(height: Space.s12),
        Align(
          alignment: Alignment.centerLeft,
          child: Btn.primary(
            _fresh ? _freshLabel : _continueLabel,
            onTap: submit == null || !widget.enabled
                ? null
                : () => submit(
                    RecoveryChoice(
                      model: _model.text.trim(),
                      fresh: _fresh,
                      rounds: rounds,
                    ),
                  ),
          ),
        ),
      ],
    );
  }
}
