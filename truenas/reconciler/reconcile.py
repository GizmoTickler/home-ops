#!/usr/bin/env python3
"""nas01 app reconciler entrypoint and test compatibility facade (implementation lives in reconciler_lib/).

Runs on nas01 (invoked by /mnt/flashstor/secure-scripts/apps-gitops/launcher.py). git is the source of truth;
nas01 converges to it via one single-writer reconciler coordinator.
"""
import os, sys

_DIR = os.path.dirname(os.path.abspath(__file__))
if _DIR not in sys.path:
    sys.path.insert(0, _DIR)

import reconciler_lib.runtime as _rt  # noqa: E402

_rt.__file__ = os.path.abspath(__file__)

if __name__ == "__main__":
    sys.exit(_rt.main())

sys.modules[__name__] = _rt
