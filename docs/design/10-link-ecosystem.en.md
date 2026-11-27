# Engineering and distribution plan for link plugins

[中文](10-link-ecosystem.md) · [Issue #10](https://github.com/arcships/rutis-network/issues/10)

Draft, 2026-11-27. The compatibility baseline is rutis 0.23.0, session protocol 5 and plugin API 3. This PR proposes the design for review. Each stage will have its own implementation PR after agreement.

## Scope and priorities

**Go and TypeScript come first.** Go adapter authors need a reusable bridge. Operators need a binary that their host can load. Use TypeScript to develop the Node reference plugin and browser adapter. Browser applications load compiled ESM, and TypeScript consumers get declarations generated from the implementation. Hosts in every language use the same `rutis.json` row configuration. [TCP integration](06-link-tcp-integration.en.md) · [WebRTC integration](08-link-webrtc.en.md)

There are seven independent Go modules under `link/`, each with its own Go version requirement. The private TCP npm project already runs strict type checking, and the private WebRTC project builds browser ESM. Cargo continues to verify the existing Rust listen example. New Python and Rust templates, followed by Swift and Kotlin support, can be scheduled as adapters need them. [TCP project](../../link/tcp/package.json) · [WebRTC project](../../link/webrtc/package.json) · [Rust example](../../examples/rust-listen/Cargo.toml)

Issue #1 defines the session handoff and conformance contract. Issue #10 turns that contract into developer commands, CI, installable artifacts, templates and a catalogue. The adapter owns transport, admission and reconnects. The session handles the identity registered by the peer, service discovery and RPC. The command names, template directories, tags and release files below are proposed deliverables. [Link contract](01-link-plugin-contract.en.md)

## Before Stage 1. Organize sources and migrate the TypeScript implementation

Keep each adapter under `link/<transport>/`. Go sources, `go.mod` and `cmd/` stay at the existing module paths used by dependents and release tags. Give each existing npm project its own runnable subdirectory. Of the 28 tracked `.mjs` files under `link/`, 18 live under test directories. The rest belong to WebRTC and cover six browser runtime files, the HTTP signaling client, Node pairing, the build script and the example server.

| Existing code | Proposed layout and migration |
| --- | --- |
| TCP `bridge.ts` and `dial.ts` | Both already use TS. Move their implementations to `link/tcp/node/src/`. Keep thin re-exports at the old paths, including the default plugin export from `dial.ts` and the named and type exports from `bridge.ts`, so existing host rows still load. |
| WebRTC runtime in `browser/` and `signaling.mjs` | Move the channel, link state, page endpoint, platform bindings and HTTP signaling client to strict TS in `link/webrtc/web/src/browser/`. Export `createWebRtcLink`, `createPageEndpoint` and `httpSignaling` from the browser entry point `index.mts`. |
| WebRTC `pairing.mjs` | Move to `link/webrtc/web/src/node/pairing.ts` for the example server and tests. Keep the browser entry limited to client APIs so that only the server loads the pairing service’s Node crypto dependency. Put signaling types used by both targets in `src/shared/` when needed. |
| Build, examples and tests | Move the build entry point to the package’s `scripts/` directory. Update npm scripts, esbuild aliases and injected file paths together. Keep `node example/server.mjs` as the documented launch command. Build and example scripts, Node tests and host fixtures loaded by path may continue to use handwritten `.mjs`. Update test imports and links in both guides as part of the move. |

The migration PR enables strict type checking in each package. Use DOM types for the browser target, and NodeNext with Node types for the Node target. Type the interfaces used through the pinned rutis session and codec build aliases. Keep runtime validation of HTTP and RTC inputs. esbuild bundles the TS entry point into `dist/link-webrtc.mjs` for direct import by plain JS pages. Run `tsc --emitDeclarationOnly` on the same sources to produce the declarations for Stage 3. [esbuild TypeScript support](https://esbuild.github.io/content-types/#typescript) · [TypeScript ESM extensions](https://www.typescriptlang.org/docs/handbook/release-notes/typescript-4-7.html#new-file-extensions)

**Migration acceptance.** From a clean checkout, run type checks in both packages and the existing unit and host integration tests. Complete a TCP RPC through the original `dial.ts` host row. Start the WebRTC example with the documented command, import all three public functions from the original browser URL, and exercise browser-to-browser and native-to-browser RPC and forced TURN relay. Stage 1 then exposes the package checks at the repository root.

### Running during development and packaging

Stock rutis-host 0.23.0 provides two development loops through `rutis-host dev <dir>`. A Node project loads from source and reloads affected rows when source files change. A Go project recompiles on source changes and restarts its runtime after a successful build. On both current mixed TCP and WebRTC directories, the host selected `package.json` first and exited because it could not find a Node entry. Move each npm manifest, lockfile and the tests that use its dependencies into the subproject below. Update cross-package fixture references, install commands and both guides. [rutis 0.23 development mode](https://github.com/arcships/rutis/blob/v0.23.0/crates/rutis-host/src/dev.rs) · [Host project and entry detection](https://github.com/arcships/rutis/blob/v0.23.0/crates/rutis-host/src/languages/mod.rs)

| Target | Development entry and behavior on edit | One-shot build and packaging |
| --- | --- | --- |
| Go plugins, starting with TCP | Run `rutis-host dev link/tcp` from the repository root. Provide the row settings and loopback peer configuration through the development project's `rutis.dev.json`. Saving Go source makes the host rebuild and restart the plugin runtime. | Run `go build` on `./cmd/link-tcp` in its module. Stage 3 archives that binary for its platform and loads it from `runtimes.go.dir` in a separate host. |
| TCP Node/TS reference plugin | Run `npm run dev` under `link/tcp/node/` to invoke the pinned `rutis-host dev .`. Export the existing dial plugin from `src/index.ts` so the host loads TS directly using the development row configuration. | `npm run build` type-checks and writes runtime ESM and declarations to the package’s `dist/`. Use the `prepack` hook to build from a clean source tree during `npm pack`, then install the tarball and mount its package by name in the host. Declare imports left external by the build as runtime dependencies. The TCP reference package stays private. |
| WebRTC browser client | Run `npm run dev` under `link/webrtc/web/` to start the loopback example with same-origin signaling. esbuild watches its inputs. After a successful rebuild the server serves the new module and the page refreshes automatically. In dev mode serve current HTML and the latest successful bundle on request. Restart the example service when Node pairing code changes. | `npm run build` emits browser ESM and declarations from the same entry. `npm pack` builds and packages the release files from `web/dist/`. The build also places the same ESM at the existing example path `link/webrtc/dist/link-webrtc.mjs`. |

Deliver local development and one-shot builds during the source migration, exercise them in temporary projects in Stage 2 and publish in Stage 3. For development acceptance, complete an RPC, edit an observable behavior, wait for row reload or page refresh, reconnect and verify the call returns the new result. Reloading a transport row or refreshing a page ends the connection it owns. Reconnection follows the adapter configuration. A failed browser build reports its error and keeps serving the previous successful bundle. Fixing it resumes live reload. For package acceptance, install the artifact outside the checkout and complete the same RPC with only runtime dependencies. Exercise both paths in each template. [esbuild watch and live reload](https://esbuild.github.io/api/#live-reload)

## Stage 1. Standardize formatting and local checks

- Provide `fmt`, `fmt-check` and `check` entry points at the repository root. Only `fmt` writes source files. `fmt-check` lists files needing formatting and exits nonzero on differences. `check` reuses that check, then runs static checks and fast tests.
- Use `gofmt -w` to format tracked `.go` files. In check mode run `gofmt -l` and fail if it lists any files. Run `go vet` and unit tests at each plugin's `go.mod`, respecting its declared Go version. A local workspace may link modules for development. Stage 3 verifies installation against published dependencies.
- Pin Prettier and its rules for handwritten `.ts`, `.mts` and `.mjs`, excluding dependencies and build artifacts. Keep the TCP reference project's minimum Node version of 22.18. Extract shared strict settings from the package configs and check the browser and Node targets separately. Report formatting and `tsc --noEmit` separately. Isolate initial formatting changes in a separate commit for review.

**Acceptance.** A checkout can check all seven Go modules and both JS/TS projects from the root. A deliberate formatting difference lists the file and leaves the working tree untouched. Running `fmt` makes the check pass. Introduce a type error in the TCP reference plugin and in the WebRTC browser implementation in turn. The root type check must identify the affected file in each case.

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
| Browser TypeScript consumers | Point `exports` to the browser ESM and to the `.d.mts` entry generated from the same TS source. Ship all declarations referenced by that entry. Cover `createWebRtcLink`, `createPageEndpoint`, `httpSignaling`, configuration, status, signaling and callbacks. Keep the TCP TypeScript reference project for interoperability tests | Install the `npm pack` tarball in an independent TS project, import by package name and pass strict type checking. Run an RPC in a real browser using that package |

Version plugins and npm packages using semantic versions. Release notes name the tested rutis minor version, protocol and platforms separately. The submodule tag lets Go resolve the version of the corresponding directory. PRs build and install candidate artifacts. Maintainers release from a commit that passed CI, with external installation checks against the same version and artifacts. Before public distribution, add the proposed MIT LICENSE and confirm the copyright holder. Public npm publication also requires a confirmed package name and scope permissions. Add a platform to the release matrix once the download, host-load and RPC check passes there. [Go module version mapping](https://go.dev/ref/mod#vcs-version) · [Go plugin installation](06-link-tcp-integration.en.md) · [npm pack](https://docs.npmjs.com/cli/v11/commands/npm-pack)

## Stage 4. Ship Go and TypeScript scaffolds

Use the Stage 2 conformance cases and the distribution paths verified in Stage 3. Provide two copyable templates in this repository.

| Template | What the author gets |
| --- | --- |
| `templates/go/link-echo/` | An independent `go.mod` depending on the released TCP bridge, an adapter, a `cmd/link-echo` entry point, sample configuration and tests |
| `templates/typescript/link-echo/` | An independent Node ESM package, lockfile, strict `tsconfig`, a plugin entry point under `src/`, sample configuration and tests under `test/`. Include the bridge from the tested TCP reference source so the copied project can build on its own |

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
