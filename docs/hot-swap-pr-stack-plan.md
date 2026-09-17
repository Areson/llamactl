# Canary → Stacked PR Plan

**Repo:** `E:\OpenClaw_Projects\llamactl-src`
**Fork:** `Areson/llamactl` · **Upstream:** `lordmathis/llamactl`
**Canary (original):** `canary/hot-swap-review` @ `7f57756` (pre-rebase backup tag `backup/canary-pre-rebase` @ `33bb454`)
**Rebased + reordered:** `canary/hot-swap-review-v2` @ `0d3abda`
**Written:** 2026-09-17

---

## 1. What changed upstream (verified)

PR #252 **MERGED** as merge commit `34f57e4` (2026-09-17). Upstream then landed
4 dependabot bumps + a Vite/vitest config chore, moving `main` from `a32ef31`
(v0.21.1) → `1ef33e2` (**v0.21.2**).

Canary was based on the *old* `a32ef31`, so it had genuinely diverged:
**13 ahead / 13 behind**.

Canary carried a **local re-implementation** of what upstream merged:
canary `f0cfaa9` == upstream `90786eb` + `9cbaac1` + `b9bac16` (squashed).
`ce87038` and `c13238a` were also already upstream as identical SHAs and are now
ancestors of `origin/main` via the #252 merge — **they must not be re-sent.**

> Fetch gotcha: a plain `git fetch origin --prune` silently did **nothing** here and
> left `origin/main` stale at `a32ef31`. Use an explicit refspec:
> `git fetch origin '+refs/heads/main:refs/remotes/origin/main'`.

---

## 2. Rebase: conflicts found and how they were resolved

A trial rebase in a throwaway worktree predicted the outcome exactly. **Two commits
conflicted — both because canary re-introduced work upstream had deliberately removed.**

### Conflict A — the hot-swap core commit (`c034391`)

| File | Resolution |
|---|---|
| `pkg/instance/process.go` | **Kept BOTH**: canary's `IsAdopted()` early-return **and** upstream's generation capture (`cmd := p.cmd`, `stdin := p.stdin`) |
| `pkg/instance/process_group_windows.go` | **Took upstream's no-op `signalStop`**; dropped canary's `CTRL_C_EVENT` call |

⚠ **The trap:** this commit re-added the non-functional Windows Ctrl-C stop path and
the un-captured `cmd`/`stdin` — the exact two things PR #252 removed (one was a
*blocking* review comment: "`stop()` can kill a newer generation"). A blind
`git rebase` would have **silently regressed your own merged fix.**

Also removed a now-dead `var (kernel32, procGenerateEvent)` block this commit had
introduced (upstream deleted it); folded in via autosquash. `syscall` stays — still
used by `setProcAttrs`.

### Conflict B — the #252 re-implementation commit (`f0cfaa9`)

`pkg/manager/manager.go`, 2 hunks. Upstream's merged side already contains the correct
**hoisted pre-pass** that clears persisted "running" state before the limit checks
(`manager.go:367-377`). Canary's per-branch `markStopped` calls were therefore
**redundant duplicates** → **took upstream's side, dropped canary's.**

### Reorder

The original order put the idle-clock/ordering fixes *after* the SSE commits they
should sit under. Reordered via `rebase -i` into dependency order. **Resulting tree
hash is byte-identical to the pre-reorder rebased tree** — pure reordering.

---

## 3. Verification (all measured, not assumed)

| Check | Result |
|---|---|
| `go build ./...` | ✅ clean |
| `go vet ./...` | ✅ clean |
| Reordered tree == pre-reorder tree | ✅ identical (`e98ee76`) |
| `procGenerateEvent` regression reintroduced? | ✅ **0 occurrences** |
| Generation capture present in `stop()`? | ✅ present |
| Boot-limit pre-pass present? | ✅ present |
| `IsAdopted()` path preserved? | ✅ present |
| Payload vs `origin/main` | 104 files, ~10,617 insertions |
| Every stack level compiles | ✅ all 8 |

**Test suite vs documented baseline** (`llamactl-review-252/testsuite-{head,base}.txt`) —
failure set is **identical to baseline**: zero regressions.

- `pkg/config` `TestLoadConfig_Defaults` — host path default (Windows vs Linux)
- `pkg/database`, `pkg/manager` — `CGO_ENABLED=0` go-sqlite3 stub (environment)
- `pkg/server` `TestEnsureInstanceRunning_StartsWhenStopped` + `_ConcurrentStartsOnlyOnce`
  — **proven pre-existing**: reproduced identically on pre-rebase canary
  (`backup/canary-pre-rebase`), so not caused by the conflict resolution.

Passing: `pkg/instance`, `pkg/hotswap`, `pkg/stats`, `pkg/backends`, `pkg/models`,
`pkg/validation`.

> Note: `pkg/server` tests need `webui/dist/` present (untracked build artifact).
> Copy it into a fresh worktree or you get `[setup failed]`.

---

## 4. The stack (built and verified)

Every branch is a **contiguous prefix** of the reordered history off `origin/main`,
so `git diff <prev>...<branch>` shows **only that level's own commits**.

| # | Branch | Tip | Introduces | Incremental diff |
|---|---|---|---|---|
| 1 | `fix/idle-clock-and-ordering` | `6080e8a` | idle clock + running-first ordering | 4 files, +209/−2 |
| 2 | `feat/ui-auto-refresh` | `e10e327` | UI auto-refresh on state change | 2 files, +167/−14 |
| 3 | `feat/instance-events-sse` | `74940d7` | SSE events endpoint | 13 files, +561/−6 |
| 4 | `feat/model-throughput-stats` | `5453887` | throughput stats/badge/history | 15 files, +1028 |
| 5 | `feat/instance-card-layout` | `bb6ab0e` | instance card layout | 1 file, +15/−11 |
| 6 | `feat/hot-swap-core` | `1661b0a` | Windows hot-swap (1 commit) | 43 files, +3091/−40 |
| 7 | `fix/hotswap-hardening` | `a87ed1c` | B-capability/rollback/exit-once | 2 files, +70/−6 |
| 8 | `chore/hotswap-tests-docs` | `5456426` | tests/docs/e2e/plan | 8 files, +2285/−1 |

Level 6 was squashed from 3 commits (feature → refactor → cleanup) into one logical
change. The `poc/` probe directories (~2,600 lines) were **dropped** from level 8 —
they were exploratory scratch; their findings live in `docs/hot-swap-drain-poc.md`.
Level 8 therefore went from 5,490 → 2,285 lines.

`fix/instance-lifecycle-races` (PR #252) is **already merged** — it is level 0 and is
*not* part of this stack.

### Rationale for this ordering

- **#1 first**: smallest diffs, zero architectural risk → fast merge, builds trust.
- **#2 → #3**: auto-refresh lands first, then the SSE transport it consumes.
- **#4 → #5**: throughput data, then the badge that renders it (`#5` is pure JSX).
- **#6 is the keystone**: `pkg/hotswap/*`, `cmd/server/main.go`,
  `pkg/manager/hotswap*.go`, `pkg/server/handlers_hotswap.go`.
- **#7 must follow #6**: hardening is meaningless without the hot-swap core.
- **#8 last**: docs/e2e describe the finished feature.

### ~~Trim #8 before opening~~ — DONE

The `poc/` directories were dropped; level 8 is now 2,285 lines of docs + tests +
e2e harness.

### Commit hygiene in #6 — DONE

Squashed to one commit (`1661b0a`).

---

## 5. Next steps

1. **Push** — the 9 branches are already pushed to `Areson/llamactl`; re-push with
   `--force-with-lease` after the squash + poc drop:
   ```bash
   cd E:/OpenClaw_Projects/llamactl-src
   for b in canary/hot-swap-review-v2 fix/idle-clock-and-ordering feat/ui-auto-refresh \
            feat/instance-events-sse feat/model-throughput-stats feat/instance-card-layout \
            feat/hot-swap-core fix/hotswap-hardening chore/hotswap-tests-docs; do
     git push --force-with-lease fork "$b"
   done
   ```
2. **Open PRs bottom-up**, each targeting the branch below it (not `main`) until the
   one below merges, then retarget. Verify no level re-sends merged work:
   ```bash
   for b in fix/idle-clock-and-ordering feat/ui-auto-refresh feat/instance-events-sse \
            feat/model-throughput-stats feat/instance-card-layout feat/hot-swap-core \
            fix/hotswap-hardening chore/hotswap-tests-docs; do
     echo "== $b"; git log --oneline origin/main..$b
   done
   ```
   Each must list **only** its own commits.
3. **Decide the `poc/` directories** for #8.
4. **Flag the #3 → #2 dependency** in the #3 PR body: `feat/instance-events-sse`
   introduces the SSE endpoint the throughput badge consumes.

---

## 6. Recovery

```bash
# Original canary (pre-rebase)
git switch --detach backup/canary-pre-rebase   # 33bb454
# Rebased canary (pre-reorder)
git switch canary/hot-swap-review              # 7f57756
# Rebased + reordered (current work)
git switch canary/hot-swap-review-v2           # 0d3abda
```

---

## 7. Execution checklist

- [x] Force-refresh `origin/main` to `1ef33e2` (plain fetch was stale)
- [x] Backup tag `backup/canary-pre-rebase` @ `33bb454`
- [x] Trial rebase in throwaway worktree; conflicts characterised
- [x] Rebase onto `origin/main`, hand-resolving both conflicts
- [x] Build + vet clean; tests match baseline, zero regressions
- [x] Reorder commits into dependency order (tree-identical)
- [x] Create stack branches #1–#8
- [x] Verify each level's diff contains only its own commits
- [x] Verify every stack level compiles
- [x] Squash level 6 to one commit
- [x] Drop `poc/` from level 8 (5,490 → 2,285 lines)
- [x] Push all 9 branches to fork
- [ ] Force-push after squash + poc drop (`--force-with-lease`)
- [ ] Open PRs bottom-up, linking each to the one below
