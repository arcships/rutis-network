# Engineering and distribution plan for link plugins

[中文](10-link-ecosystem.md) · [Issue #10](https://github.com/arcships/rutis-network/issues/10)

Draft, 2026-11-27. The compatibility baseline is rutis 0.23.0, session protocol 5 and plugin API 3. This PR proposes the design for review. Each stage will have its own implementation PR after agreement.

## Scope and priorities

**Go and TypeScript come first.** Go adapter authors need a reusable bridge. Operators need a binary that their host can load. TypeScript serves authors of Node plugins and browser applications that call the WebRTC API. Browser applications need ESM and accurate declarations and can use the existing JavaScript implementation. Hosts in every language use the same `rutis.json` row configuration. [TCP integration](06-link-tcp-integration.en.md) · [WebRTC integration](08-link-webrtc.en.md)

There are seven independent Go modules under `link/`, each with its own Go version requirement. The private TCP npm project already runs strict type checking, and the private WebRTC project builds browser ESM. Cargo continues to verify the existing Rust listen example. New Python and Rust templates, followed by Swift and Kotlin support, can be scheduled as adapters need them. [TCP project](../../link/tcp/package.json) · [WebRTC project](../../link/webrtc/package.json) · [Rust example](../../examples/rust-listen/Cargo.toml)

Issue #1 defines the session handoff and conformance contract. Issue #10 turns that contract into developer commands, CI, installable artifacts, templates and a catalogue. The adapter owns transport, admission and reconnects. The session handles the identity registered by the peer, service discovery and RPC. The command names, template directories, tags and release files below are proposed deliverables. [Link contract](01-link-plugin-contract.en.md)

## Stage 1. Standardize formatting and local checks

- Provide `fmt`, `fmt-check` and `check` entry points at the repository root. Only `fmt` writes source files. `fmt-check` lists files needing formatting and exits nonzero on differences. `check` reuses that check, then runs static checks and fast tests.
- Use `gofmt -w` to format tracked `.go` files. In check mode run `gofmt -l` and fail if it lists any files. Run `go vet` and unit tests at each plugin's `go.mod`, respecting its declared Go version. A local workspace may link modules for development. Stage 3 verifies installation against published dependencies.
- Pin Prettier and its rules for handwritten `.ts` and `.mjs`, excluding dependencies and build artifacts. Keep the TCP reference project's minimum Node version of 22.18 and extract its strict TypeScript settings. Use NodeNext for Node plugins and DOM types for browser consumer tests. Report formatting and `tsc --noEmit` separately. Isolate initial formatting changes in a separate commit for review.

**Acceptance.** A checkout can check all seven Go modules and both JS/TS projects from the root. A deliberate formatting difference lists the file and leaves the working tree untouched. Running `fmt` makes the check pass. A type error in the TCP reference plugin fails type checking even when formatting passes.

## Stage 2. Run the same checks on pull requests

Use the root commands from Stage 1. Linux is the first required platform. Start from a clean runner, select Go toolchains per module, install npm dependencies with `npm ci` against the lockfiles, and publish separate job results.

| Job | What runs | Passing result on the PR |
| --- | --- | --- |
| Format, types and build | Reuse `check`, build the Go plugin commands and the WebRTC browser module, and check internal documentation links | Every module and package has a result identifying its path on failure |
| Go and TS conformance | Run the applicable TCP cases below against both implementations. Run `go test -race` on supported Go runners | Both implementations meet the same assertions, with applicability recorded |
| Host and browser integration | Provision matching `rutis-host`, Chromium, Firefox and loopback TURN. Exercise bidirectional RPC, forced relay and short-lived credential renewal | Required cases execute. Missing tools or a skipped required case fail the job |

Run the basic checks on every PR. Run integration jobs for changes to the corresponding plugin, shared bridge, fixtures or CI configuration. Run the full account-free matrix on the main branch. List tests requiring a real tailnet or vendor account separately for manual verification, recording platform and outcome. Keep the Rust example covered by `cargo test --locked`. [Existing test entry points](../guide/link-plugins.en.md) · [WebRTC integration cases](../../link/webrtc/test/hosts.test.mjs)

**Acceptance.** A PR from a clean checkout shows fast checks and integration results separately. A shared TCP bridge change exercises its dependent plugins against the checkout via a local workspace. Stage 3 tests installation against released dependencies separately. A formatting error and a missing required host each produce a failure in the corresponding job.

## Stage 3. Publish plugins and install them outside the repository

Use the builds and tests from Stage 2. Pilot both Go distribution paths with TCP, then use WebRTC to verify the TypeScript consumer path.

| Audience | Deliverable and version | Acceptance in a clean environment |
| --- | --- | --- |
| Go adapter authors | Publish `github.com/arcships/rutis-network/link/tcp` with submodule tags of the form `link/tcp/vX.Y.Z`. Then replace SSH's `v0.0.0` dependency with the released version, remove its `replace` pointing to `../tcp`, and update the TCP pseudo-versions pinned by gRPC, WebRTC and Tailscale | In a temporary external module with `GOWORK=off`, import the bridge at its released version and build it. Run `go install github.com/arcships/rutis-network/link/tcp/cmd/link-tcp@vX.Y.Z` with the released version and no local directory dependencies |
| Host operators | Build `link-tcp` from the same tag. Attach an archive named by plugin, version, OS and architecture to GitHub Releases, alongside SHA-256 checksums, the compatible rutis version and minimal configuration. Start with Linux/amd64 | Download and verify the archive. Put the executable in the directory configured by `runtimes.go.dir`, load `go:link-tcp-dial` in the stock host and complete a remote RPC |
| Browser TypeScript consumers | Package WebRTC browser ESM with matching `.d.mts` declarations. Expose type and runtime entries through `exports`. Cover `createWebRtcLink`, `createPageEndpoint`, `httpSignaling`, configuration, status, signaling and callbacks. Keep the TCP TypeScript reference project for interoperability tests | Install the `npm pack` tarball in an independent TS project, import by package name and pass strict type checking. Run an RPC in a real browser using that package |

Version plugins and npm packages using semantic versions. Release notes name the tested rutis minor version, protocol and platforms separately. The submodule tag lets Go resolve the version of the corresponding directory. PRs build and install candidate artifacts. Maintainers release from a commit that passed CI, with external installation checks against the same version and artifacts. Before public distribution, add the proposed MIT LICENSE and confirm the copyright holder. Public npm publication also requires a confirmed package name and scope permissions. Add a platform to the release matrix once the download, host-load and RPC check passes there. [Go module version mapping](https://go.dev/ref/mod#vcs-version) · [Go plugin installation](06-link-tcp-integration.en.md) · [npm pack](https://docs.npmjs.com/cli/v11/commands/npm-pack)

## Stage 4. Ship Go and TypeScript scaffolds

Use the Stage 2 conformance cases and the distribution paths verified in Stage 3. Provide two copyable templates in this repository.

| Template | What the author gets |
| --- | --- |
| `templates/go/link-echo/` | An independent `go.mod` depending on the released TCP bridge, an adapter, a `cmd/link-echo` entry point, sample configuration and tests |
| `templates/typescript/link-echo/` | An independent Node ESM package, lockfile, strict `tsconfig`, plugin entry point, sample configuration and tests. Include the bridge from the tested TCP reference source so the copied project can build on its own |

Both templates connect over loopback TCP with temporary mTLS credentials and hand off through `rutis/sessions.open(far)`. Each README covers installation, changing the transport, startup, errors and limits. For acceptance, CI copies each template outside the checkout, renames the plugin, installs dependencies, builds, starts the host, calls echo and runs the shared conformance cases. Additional language templates follow these two working examples. [TCP reference implementation](../../link/tcp/bridge.ts) · [Plugin author guide](../../.agents/skills/rutis-link-plugin/SKILL.md)

## Conformance and management boundaries

Go and TS use the same case names and expected outcomes, implemented in their native test runners. Byte samples and boundary sizes can be shared as data. Language-specific fixtures control the timing of slow writes, disconnects and unloads.

| Case | Assertion |
| --- | --- |
| Message boundaries and ordering | Deliver 1000 messages in order, including an empty message. A single 16 MiB TCP frame passes, and one byte over the limit closes the connection cleanly |
| Backpressure and asynchronous writes | A slow receiver blocks sending. The sender waits for write completion, the bridge's own queue stays bounded, and all messages arrive after the receiver resumes. The rutis 0.23 host owns the total non-Rust session outbound queue |
| EOF and unload | EOF maps to `near.end(done)` and closes both directions. Session unload closes the transport. Cleanup is registered and executed once |
| Admission, identity and listeners | Admission failure makes zero calls to `open`. Use the host to verify remote service addressing and `<id>:<plugin>` naming. Listeners supporting multiple connections mount one session per admitted connection |
| Same-ID replacement | Verify with the Rust handshake example. In 0.23 Go/TS cannot obtain the greeted peer ID from `open`. Mark same-ID replacement and that status field as inapplicable |
| Errors | Assert stable error codes on truncation, oversize and reset. Logs omit credentials |

Composition uses `rutis.json` rows, sessions and service names. Use `rutis/loader` to add and remove rows. Each plugin declares its configuration schema and can expose a row-scoped control service reporting connection phase, an observable direct or relay route, last error and retry count. Business plugins call services by name. Each adapter owns its credentials, retries and optional `doctor` tool. WebSocket uses core support, and plain HTTP can carry signaling. [Link contract and host limits](01-link-plugin-contract.en.md)

## Catalogue and follow-up coverage

Maintain the bilingual plugin catalogue. For each adapter record its channel, admission, languages, prerequisites, use cases, priority (T1/T2/T3) and status (planned, prototype or available). Link its issue, design, example and verified installation path. Keep at least one copyable configuration each for SSH, Tailscale and Cloudflare Tunnel. Mark third-party adapters with the conformance cases they actually pass. [Plugin catalogue](../guide/link-plugins.en.md)
