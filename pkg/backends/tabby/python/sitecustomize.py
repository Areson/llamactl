"""llamactl TabbyAPI GET /slots launch shim.

Loaded automatically when this directory is on PYTHONPATH (site imports
sitecustomize at interpreter start). Wraps endpoints.server.setup_app so the
FastAPI app gains a llama.cpp-shaped /slots route before uvicorn serves.

Tabby files on disk stay stock — this module lives in llamactl and is injected
only for processes llamactl starts. Idempotent: if /slots is already registered
(e.g. an older local router.py patch), the shim leaves it alone.

Widget contract (icue-llamactl-widget slotView / ingestSlots):
  id, id_task, is_processing,
  n_prompt_tokens, n_prompt_tokens_processed, n_prompt_tokens_cache,
  next_token[0].n_decoded   (monotonic per-job; widget diffs for live t/s)
"""

from __future__ import annotations

import builtins
import sys

_PATCHED_ATTR = "_llamactl_slots_shim"


def _slots_payload():
    from common.status_display import status_display

    slots = []
    for slot_id, (request_id, job) in enumerate(list(status_display.jobs.items())):
        generating = job.stage == "generating"
        slots.append(
            {
                "id": slot_id,
                "id_task": request_id,
                "is_processing": job.stage in ("prefill", "generating"),
                "n_prompt_tokens": job.prompt_tokens,
                "n_prompt_tokens_processed": (
                    job.prefill_tokens if job.stage == "prefill" else job.prompt_tokens
                ),
                "n_prompt_tokens_cache": job.cached_tokens,
                "next_token": [{"n_decoded": job.gen_tokens}] if generating else [],
            }
        )
    return slots


def _app_has_slots(app) -> bool:
    for route in getattr(app, "routes", None) or []:
        if getattr(route, "path", None) == "/slots":
            return True
    return False


def _install_on_app(app):
    if _app_has_slots(app):
        return app

    async def list_slots():
        return _slots_payload()

    app.add_api_route("/slots", list_slots, methods=["GET"])
    return app


def _patch_setup_app(mod) -> None:
    if getattr(mod, _PATCHED_ATTR, False):
        return
    orig = getattr(mod, "setup_app", None)
    if orig is None:
        return

    def setup_app(host=None, port=None):
        return _install_on_app(orig(host, port))

    setup_app.__wrapped__ = orig  # type: ignore[attr-defined]
    mod.setup_app = setup_app
    setattr(mod, _PATCHED_ATTR, True)
    try:
        print("[llamactl] TabbyAPI /slots shim active", file=sys.stderr, flush=True)
    except Exception:
        pass


def _maybe_patch(name, module, fromlist) -> None:
    targets = []
    if name == "endpoints.server":
        targets.append(module)
    elif name == "endpoints" and fromlist and "server" in fromlist:
        srv = getattr(module, "server", None)
        if srv is not None:
            targets.append(srv)
    for target in targets:
        if hasattr(target, "setup_app"):
            _patch_setup_app(target)


_orig_import = builtins.__import__


def _import(name, globals=None, locals=None, fromlist=(), level=0):
    module = _orig_import(name, globals, locals, fromlist, level)
    try:
        _maybe_patch(name, module, fromlist)
    except Exception as exc:  # never break Tabby startup
        try:
            print(
                f"[llamactl] TabbyAPI /slots shim patch skipped: {exc}",
                file=sys.stderr,
                flush=True,
            )
        except Exception:
            pass
    return module


builtins.__import__ = _import