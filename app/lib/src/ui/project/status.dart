import '../../model/thread_ref.dart' as model;
import '../kit/kit.dart';

/// The kit's status for a thread's, in one place so every project screen maps
/// it the same way (design-system.md §6).
Status threadStatusOf(model.ThreadStatus status) => switch (status) {
  model.ThreadStatus.needsYou => Status.needsYou,
  model.ThreadStatus.running => Status.running,
  model.ThreadStatus.ready => Status.ready,
  model.ThreadStatus.failed => Status.failed,
  model.ThreadStatus.idle => Status.idle,
};
