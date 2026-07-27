# Apply execution design

Status: discussion baseline, not implemented.

This document records the agreed direction for two complementary `envinit apply` entry points:

1. an interactive Apply TUI for single-node field work;
2. a fail-closed unattended mode for fleet automation.

Both entry points must use the same runner, stage order, checkpoint semantics, structured events, safety validation, and final result format. The TUI is a presentation and decision provider, not a separate execution engine.

## 1. Current foundation

The existing runner already provides useful foundations:

- ordered stages: `software`, `ofed`, `network`, `xre`, `xdr`, `firmware`, `container`, `mlxconfig`, `sysctl`, `kernel`, `post`;
- atomic progress in `/var/lib/envinit/apply-progress.json`;
- configuration fingerprints and automatic invalidation of stale progress;
- restart and stage-level resume;
- plan descriptions grouped by stage;
- centralized command and log helpers;
- existing NIC Binding Review and MST Review TUIs.

The current runner still treats output as text and requires a TTY for every real apply. NIC, MST, and power-action decisions are implemented independently. These constraints must be refactored before either a unified Apply TUI or unattended mode is safe.

## 2. Shared execution architecture

Introduce a structured event stream instead of deriving state by parsing log text. The event model should cover:

- apply started/completed/failed;
- checkpoint loaded/invalidated/saved;
- stage pending/started/completed/failed/skipped/resumed;
- command started/completed/failed;
- stdout/stderr chunks;
- user decision requested/resolved;
- stop-after-stage requested;
- final result persisted.

Plain text output remains supported for scripts and redirected logs. TUI and JSONL output consume the same events.

Replace scattered interactive behavior with a decision-provider interface. The interactive provider renders modal views; the unattended provider accepts only deterministic, pre-authorized decisions and otherwise fails before mutation.

Use an explicit interaction mode rather than accumulating booleans:

- `interactive`: unified Apply TUI owns the terminal;
- `plain-interactive`: current-style text output with explicit prompts, retained for compatibility if needed;
- `unattended`: no TTY or stdin dependency, strict fail-closed validation.

## 3. Interactive Apply TUI

### 3.1 Setup flow

Use `Target -> Stages -> Resume -> Review`:

- Target shows inventory identity, hostname, management address, platform, package manager, and network backend.
- Stages selects `all` or explicit stages and explains that explicit stages do not update the full-run checkpoint.
- Resume shows checkpoint fingerprint, completed stages, current/failed stage, last error, and update time. Restarting from `software` requires confirmation.
- Review reuses the current plan/describe data and starts mutation only after confirmation.

### 3.2 Runtime pages

Provide three pages:

- `Overview`: all stages with `PENDING/RUNNING/PASS/FAIL/SKIP/RESUME`, overall stage count, current stage, and elapsed time;
- `Current Stage`: structured steps, current command, spinner or real progress when measurable, and live output;
- `Raw Logs`: complete stdout/stderr with paging, horizontal scrolling, and error navigation.

The footer stays pinned to the terminal bottom and adapts to terminal size. Reuse the established paging convention: `PgUp/PgDown` for main content and `Ctrl+U/Ctrl+D` for a details pane.

### 3.3 Unified modal decisions

The Apply TUI must be the only owner of raw mode and the alternate screen. Existing NIC Binding Review, MST Review, restart confirmation, and post power confirmation become modal views inside the same program. Nested Bubble Tea programs must not compete for `/dev/tty`.

### 3.4 Safe stop semantics

Apply is mutating and must not copy Check's immediate-abort behavior:

- offer `stop after current stage` as the normal stop operation;
- allow exit after completion or failure;
- require a high-risk confirmation for emergency interruption;
- do not offer ordinary hard cancellation during OFED/XRE installation, firmware updates, network cutover, kernel configuration, or power actions;
- retry a failed stage only with the same semantics as rerunning apply from the saved checkpoint.

Do not display fabricated percentages. Use determinate progress only when a stage exposes a real step count; otherwise show stage state, current command, spinner, and elapsed time.

## 4. Unattended Apply

### 4.1 Meaning

`unattended` means no prompts and no TUI. It does not mean no output. Every run must remain observable through human-readable logs and optionally JSONL events.

Suggested invocation:

```bash
sudo ./env_init apply \
  --inventory planning/inventory.csv \
  --bundle planning/bundle.json \
  --host node-001 \
  --unattended \
  --result-file /var/log/envinit/apply-result.json
```

The per-node executor stays single-host. Fleet concurrency, batching, SSH transport, and rollout policy should initially remain the responsibility of Ansible or another orchestrator rather than being mixed into the local runner.

### 4.2 Fail-closed preflight

Before the first mutation, unattended mode validates all decisions needed by every selected stage. Any ambiguity aborts the run with a non-zero exit code and a structured reason.

At minimum validate:

- root privileges, supported platform, required files, artifacts, checksums, free space, and commands;
- an exact inventory target; fleet callers should pass `--host` explicitly;
- exact management and RDMA NIC bindings, preferably using inventory MAC addresses;
- deterministic MST device correlation with the confirmed RDMA interfaces;
- checkpoint ownership and configuration fingerprint;
- network-cutover policy and the risk of losing the controlling SSH path;
- post power policy;
- selected-stage dependencies;
- absence of another active apply process.

Add a preflight-only mode so a fleet can validate all nodes before applying any node.

### 4.3 Decisions that must never be guessed

- Ambiguous NIC mappings: fail and require `mgmt*_mac`/`rdma*_mac` or another exact inventory mapping.
- Ambiguous MST devices: fail instead of silently accepting a default selection.
- Power actions: require an explicit unattended authorization policy; `confirm=true` cannot be silently converted to approval.
- Disruptive management-network changes: require an explicit policy such as deferred application or authorization for network disruption.
- Clearing a checkpoint: retain explicit `--restart`; unattended mode must not infer it.

### 4.4 Fleet-operational requirements

- Add an apply lock, for example `/var/lib/envinit/apply.lock`, so duplicate orchestrator runs cannot mutate the same host concurrently.
- Persist `/var/lib/envinit/apply-result.json` with version, host, configuration SHA256, timestamps, stage results, current/last failed stage, and final error.
- Offer stable JSONL events and documented exit-code classes for validation failure, safety refusal, stage failure, and success.
- Preserve the existing checkpoint as the retry mechanism. Avoid automatic retries for destructive stages; the orchestrator may rerun the same command and resume safely.
- Define command/stage timeout policy explicitly. Do not blindly kill package, driver, firmware, network, or bootloader operations on timeout.
- Keep complete logs after terminal or SSH disconnection.

### 4.5 Recommended fleet rollout

1. Distribute a versioned binary, bundle, and inventory.
2. Run unattended preflight on every target and collect structured results.
3. Correct all inventory or safety-policy failures before mutation.
4. Apply in controlled batches (`serial`/canary), not all hosts simultaneously.
5. Gate later batches on success and post-apply health checks.
6. Rerun failed nodes using the saved checkpoint rather than automatically restarting the full workflow.

## 5. Implementation phases

### Phase A: shared runner contracts

- structured events and JSONL encoder;
- interaction/decision-provider interface;
- exported checkpoint inspection;
- apply lock and final result file;
- strict preflight API;
- keep current text behavior as a compatibility adapter.

### Phase B: unattended mode

- `--unattended` and preflight-only execution;
- deterministic NIC/MST/power/network policies;
- stable exit codes, logs, and fleet regression tests;
- validate execution with stdin closed and no `/dev/tty`.

### Phase C: Apply TUI

- setup flow and runtime pages;
- modal NIC/MST/power decisions;
- stop-after-stage and failed-stage retry;
- terminal resize and SSH/WebRelay regression tests.

Unattended mode should be implemented before or alongside the shared event layer, not as a shortcut that bypasses current prompts. The desired invariant is: interactive mode asks; unattended mode proves the answer from configuration or stops before mutation.
