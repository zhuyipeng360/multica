# Read-only default Chrome discovery for ZCode

## Scope and protocol

This branch adds read-only browser discovery to the ZCode backend. It does not modify the permission approval flow, Bash policy, browser settings, or the running Multica installation.

The [ZCode protocol schema](https://github.com/zai-org/ZCode/blob/fec975905a2abd4c07e35c40d51fa097a2258ec9/packages/shared/src/zcode-protocol/index.ts) and [browser control broker](https://github.com/zai-org/ZCode/blob/fec975905a2abd4c07e35c40d51fa097a2258ec9/apps/zcode-cli/packages/bootstrap/src/zcode-protocol/browser-control-broker.ts) define `interaction/browserList` as returning `{ "browsers": [...] }`: these are browser backend descriptors, not tabs. `browser.user.openTabs()` then issues `interaction/browserExecute` with `{ "method": "listUserTabs" }` and receives `{ "ok": true, "elapsedMs": 0, "userTabs": [{ "id": "...", "title": "...", "url": "..." }] }`. The locally installed ZCode browser client also contains `listUserTabs`. Real CLI/host interoperability has not been tested end to end.

Only `listUserTabs` is supported; all other browser commands are rejected. The existing permission rejection and unknown-method `-32601` handling remain in place. The daemon injects the Chrome provider only into the ZCode backend. The descriptor has an opaque instance ID, and a tab response is rejected if the instance changed.

## Files

- `server/pkg/agent/zcode.go`, `agent.go`, `zcode_browser.go` and adjacent tests: reverse-RPC routing, read-only provider interface, protocol validation, and tests.
- `server/internal/daemon/zcode_browser.go`, its test, and `daemon.go`: provider adapter and daemon wiring.
- `server/internal/daemon/localenv/chrome*.go` and adjacent tests: macOS default-profile process and CDP verification; `app.go` extracts the existing CDP probe for reuse.

## Safety and limitations

The macOS adapter checks the default Chrome data directory's `SingletonLock`, current user, process identity and parent, Google code signature, arguments, and ownership of a loopback listening port before any CDP HTTP request. It does not scan Koko ports or launch, restart, navigate, or control a browser. Requests are limited to `/json/version` and `/json/list`, without proxies or redirects. Only page targets are returned. URLs lose userinfo, query and fragment; non-HTTP(S) URLs are blanked. Titles and path segments may still be sensitive; do not log or publish them.

No lock yields an empty browser list. An unverifiable identity, unavailable CDP, or unsupported OS yields an error instead of a false empty result. The conservative implementation rejects explicit `--user-data-dir`, non-launchd-parent processes, and ambiguous ports; these may exclude legitimate Chrome instances. It supports macOS only.

**Critical limitation:** [Chrome's official policy](https://developer.chrome.com/blog/remote-debugging-port) says Chrome 136 and later do not honor remote debugging for the default data directory. The target default Chrome in this Mac was running without a remote-debugging port or pipe and without `DevToolsActivePort`. This implementation therefore cannot list its tabs through CDP under the current safety constraints. It does not bypass the policy or switch to a separate profile. A different authorized, reviewed design is needed for the stated goal.

## Verification performed

- Go 1.26.9; focused race tests for the Chrome detector, ZCode RPC and daemon adapter passed using fake processes and mock CDP.
- `go test ./internal/daemon/localenv -count=1` passed, and the non-macOS branch cross-compiled for Linux/arm64.
- `scripts/test-go.sh --race --only agent` failed: existing `TestCodexCancellationDuringInitializeStopsProcessImmediately` timed out waiting for a cancelled Codex result (`codex_test.go:4095`). Running that test alone also failed with the same timeout. Its cause has not been established.
- Repository `make test` was attempted after generating its isolated worktree configuration, but stopped before tests because the Docker engine was not running. No existing database was used.
- Real CLI + default Chrome end-to-end test: **not performed / not passed** because no verified CDP endpoint was available. Therefore neither the two target tabs nor the actual client/server interaction has been verified in this fork. Previous AppleScript-based results are not evidence for this CDP implementation.

To reproduce the isolated checks from `server/` with Go 1.26.9:

```sh
go test -race -p 2 -parallel 2 ./internal/daemon/localenv ./pkg/agent ./internal/daemon -run 'TestChrome|TestProbeCDP|TestParsePsArgs|TestZcode' -count=1
```

Full repository tests require the repository-managed isolated Docker/PostgreSQL setup and `make test` from the root. Do not point tests at an existing user's database.
