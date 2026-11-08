# Link plugin ecosystem: templates, conformance suite, adapter catalogue: design

Status: draft. Date: 2026-11-08. Baseline: rutis 0.21.0.
Issue: [#10](https://github.com/arcships/rutis-network/issues/10). Depends on [#1](https://github.com/arcships/rutis-network/issues/1), #3–#6.

## 1. Goal

Let a third party copy a runnable link-plugin template, verify it with one conformance suite, and avoid putting authentication/identity/reconnect into the session; let users find the right adapter in a catalogue.

## 2. Boundary with #1

- #1: the contract + one reference bridge + the conformance **contract**.
- #10: language templates, the **executable** conformance suite, the catalogue, CI, license and version policy.

## 3. Templates

Language order: TS, Python, Rust; then Go, Swift, Kotlin.

```text
templates/<lang>/
  link-echo/
    src/bridge.*      # framing + rutis/sessions.open(far) + lifecycle
    src/adapter.*     # dial/listen; admission lives here
    test/             # unit tests + conformance cases
    README.md         # install, config, errors, limits
```

Requirements:

- Only `rutis/sessions` (TS/Python) or `Session::new` (Rust); no new core API.
- `link/echo` uses localhost TCP (bind `127.0.0.1`, random port/token); WebSocket reuses the core `websocket::dial` / `websocket::listen`; no plain HTTP link template.
- Comments state: admission is in the adapter; identity comes from the far `Hello`; the session has no token.
- 0.21 async `send`, 0.20 unload order and error codes are in the template.
- `copy → change adapter → run tests`; not every language must support every adapter first.
- Every T1 adapter has at least one copy-paste example: SSH, Tailscale, Cloudflare Tunnel; examples use only `rutis.json` rows + `rutis/sessions`, no manager.

### 3.1 Management tools and composition

- Templates do not expose a cross-link manager or a global `connect` service; each adapter provides only its own row config and session.
- Composition is `rutis.json` row + `<link row>:<plugin>` + service name; business plugins do not know the link.
- Each adapter may ship an optional `status` / `doctor` / CLI as its own tool; no central manager, no central credential store.
- The row contract must expose: config schema, structured status (connected/retrying/stopped, direct/relay, last error, `Hello` id, retry count), optional `status` / `doctor`, and no secrets in logs. The `Hello` id is the id the far end registered, not an SSH user/tailnet node/token; before #1 O1 (handshake result API), the non-Rust `Hello` id field is unavailable/Rust-only.
- Dynamic changes use `rutis/loader` / `rutis.json`, not a runtime dial hub; prefer always-on rows with reconnect.

## 4. Conformance suite

One set of cases, run per language (each language has a runner; share JSON cases or the same semantics). The default transport is localhost TCP (bind `127.0.0.1`, random port/token); WebSocket cases reuse the core; plain HTTP has no cases.

| Case | Assertion |
| --- | --- |
| Boundaries/order | 1000 messages arrive in order, boundaries kept |
| Maximum message | 16 MiB passes; 16 MiB + 1 closes cleanly (16 MiB is bridge policy, not a `Channel` contract) |
| Empty message | length 0 passes |
| Backpressure | slow receiver; bridge queue stays bounded, memory does not grow |
| Async send | `send` returns a promise, session awaits; a slow write loses nothing |
| Close | far FIN → `near.end`; session unload → connection closed once |
| EOF / close | read EOF maps to `done`; the session has no half-close, no double close |
| Far id | `<id>:<plugin>` naming |
| Listen multi-connection | one session per connection |
| Same-id replacement | Rust replaces; non-Rust does not support it (`session_key` is first-wins) |
| Admission failure | fails before `open`; no session |
| Unload | session unload closes the adapter connection; cleanup registered once |
| Errors | truncated/over-limit/reset carry a code, no token |

No public account in CI; SSH/Tailscale/Cloudflare use local fakes or manual e2e steps.

## 5. Adapter catalogue

`docs/guide/link-plugins.md` (and `.en.md`) or `docs/guide/link-plugins/`, one row per adapter:

| Field | Content |
| --- | --- |
| Name | `link/ssh`, `link/tailscale`, … |
| Gives | bytes / messages / local TCP / public ingress |
| Admission | SSH host key / tailnet ACL / Access token / mTLS |
| Languages | TS / Python / Rust / Go / … |
| Prerequisite | system sshd, tailscaled, cloudflared, ADB, VPS, … |
| Scenario | personal GPU, home lab, phone, team, … |
| Priority | T1/T2/T3 |
| Status | planned / prototype / available |
| Links | issue, design, example |

Do not write "planned" as "supported".

## 6. CI

- Every language template builds/tests;
- The conformance suite runs for at least one language, then more;
- Documentation link check (`node tools/check-md-links.mjs` if the repository adopts it);
- No public account;
- Matrix: Linux required; macOS/Windows by language capability.

## 7. License and versions

- LICENSE: MIT, matching rutis.
- Version policy: adapters follow rutis minor versions; templates declare the minimum rutis version (0.21.0 onward).
- Breaking changes (such as 0.19.3's `sessions` → `rutis/sessions`) are documented in README/migration notes.
- Third-party adapters are graded by the conformance suite; no quality promise.

## 8. Non-goals

- No new core trait/API.
- No requirement that every language supports every adapter.
- No plugin marketplace; docs + templates first.
- No cross-link central management tool, global `connect` service or central credential store; management tools are per-link, optional and external.
- No duplicate of #1's contract and reference bridge.
- No CI dependency on public services.

## 9. Open questions

- O1: A JSON DSL for conformance cases, or hand-written per language? JSON is portable but limited.
- O2: Templates in this repository or a separate one? Start with `templates/` here.
- O3: One catalogue page or one page per adapter? Start with one table; split after ten.
- O4: License year/copyright holder, aligned with rutis.

## Links

- Issue [#10](https://github.com/arcships/rutis-network/issues/10)
- [Contract design](01-link-plugin-contract.en.md)
- [SSH design](03-link-ssh.en.md)
- [Tailscale design](04-link-tailscale.en.md)
- [Cloudflare design](05-link-cloudflared.en.md)
- [TCP bridge design](06-link-tcp.en.md)
- [SKILL.md](../../.agents/skills/rutis-link-plugin/SKILL.md)
