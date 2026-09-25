# TabbyAPI `/slots` launch shim

In-process shim that llamactl injects when starting a `tabby_api` instance.

## How it works

1. Go extracts this directory (embedded) to the user cache and prepends it to
   `PYTHONPATH` for the Tabby child process.
2. Python's `site` module imports `sitecustomize.py` at interpreter start.
3. The shim wraps `endpoints.server.setup_app` so the FastAPI app gets
   `GET /slots` before uvicorn serves — without editing Tabby's install tree.

## `/slots` JSON (llama.cpp-compatible subset)

Idle: `[]`

Busy (one job generating):

```json
[
  {
    "id": 0,
    "id_task": "<request-id>",
    "is_processing": true,
    "n_prompt_tokens": 1234,
    "n_prompt_tokens_processed": 1234,
    "n_prompt_tokens_cache": 100,
    "next_token": [{"n_decoded": 42}]
  }
]
```

`n_decoded` is Tabby's per-job `gen_tokens` counter. The iCUE widget diffs it
between polls (~1s) to compute live decode t/s.

## Coexistence with an old Tabby file patch

If `endpoints/core/router.py` already defines `/slots`, the shim detects the
route on the app and does not register a duplicate. Remove the file patch when
convenient; the shim alone is enough.