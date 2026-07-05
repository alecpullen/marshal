+++
name = "systematic-debugging"
description = "Systematic debugging process for bugs, test failures, and unexpected behavior"
risk = "read_only"
+++

# Systematic Debugging

When debugging, follow this process:

1. Reproduce the bug — confirm it exists and understand expected vs actual
2. Isolate — narrow to the minimal reproduction case
3. Identify root cause — don't fix symptoms
4. Fix and verify — write a test that fails before and passes after
