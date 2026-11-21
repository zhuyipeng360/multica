# Read-only default Chrome discovery for ZCode

## Scope and protocol

The daemon provides read-only discovery of the default Chrome profile to ZCode, without changing the permission approval flow, Bash policy, Chrome settings, or the running Multica installation. The [ZCode protocol schema](https://github.com/zai-org/ZCode/blob/fec975905a2abd4c07e35c40d51fa097a2258ec9/packages/shared/src/zcode-protocol/index.ts) defines `interaction/browserList` as returning browser descriptors, not tabs. `browser.user.openTabs()` then issues `interaction/browserExecute` with `listUserTabs`. Other browser commands are rejected; the existing permission rejection and unknown-method `-32601` handling remain unchanged.

## Discovery and safety

The macOS adapter identifies the owner of the default Chrome data directory's `SingletonLock`, checks the user, parent process, Google signature and command-line arguments, and rechecks the process and lock before and after reading. It rejects explicit `--user-data-dir` and non-launchd parents, which can exclude legitimate instances. No lock yields an empty browser list; an unverified identity is an error. No browser is launched, navigated, clicked, or restarted; Koko nodes are not contacted.

When a verified Chrome has a verified loopback CDP endpoint, the adapter keeps the original CDP path, limited to `/json/version` and `/json/list`. If no usable endpoint is found, it falls back to embedded JXA run via `/usr/bin/osascript`, invoked by Go with an argument vector and a 30-second timeout. JXA targets `Application(Number(pid))` and reads only window/tab IDs, titles and URLs. The ID is needed by the browser protocol; no tab control is exposed. Discovering the backend does not read tabs. Once a CDP endpoint is verified, an error reading `/json/list` is reported rather than silently switching paths.

URLs lose userinfo, query and fragment; non-HTTP(S) URLs are blanked. Titles and path segments may still be sensitive and must not be logged or committed. An opaque browser instance ID changes when the Chrome process restarts; a response from a different instance is rejected.

This implementation chooses option (b), native Go invoking embedded JXA, to avoid a Python runtime dependency. The identity and PID-targeting logic preserves the audited helper's behavior while retaining the first version's stricter signature, parent and process-start checks. CDP remains limited by [Chrome's default-profile debugging policy](https://developer.chrome.com/blog/remote-debugging-port); JXA does not change or bypass that policy.

## Automation permission

`osascript` sends Apple events to Chrome on behalf of the process that launches it. macOS may attribute the Automation permission to the launching daemon, its app/terminal, or another responsible process depending on how it is launched and signed. This test ran under the current test runner's already-authorized context; it did not establish the identity that a deployed Multica daemon will show. The installed daemon is a separately signed `multica` executable, but its eventual TCC attribution is **not verified**.

If macOS prompts, the user must decide whether to allow it; the daemon must not click the prompt. If access is denied (`-1743`), the browser command returns a specific Automation-denied error, not an empty list; `-600` reports that Chrome exited. The user can inspect **System Settings → Privacy & Security → Automation**, locate the requesting component shown there, and enable its access to Google Chrome if they choose. After deployment, verify the exact requesting component and retry; no permissions are changed by this code.

## Changed files

- `server/internal/daemon/localenv/chrome.go`, `chrome_other.go`, `chrome_jxa_darwin.go` and adjacent tests: CDP-first discovery, PID-targeted JXA fallback, sanitization and identity checks.
- `server/internal/daemon/zcode_browser.go` and its test; `server/pkg/agent/zcode_browser.go` and its test: map only fixed, sanitized Automation/exit errors into the existing response shape. Protocol routing and the approval handler are unchanged.
- `chrome_jxa_integration_test.go`: explicitly gated real-Chrome read-only adapter test; no tab titles or URLs are logged.

## Verification on this Mac

- Adapter test with `chromeintegration` and `MULTICA_RUN_REAL_CHROME_READONLY=1`: **passed**, 31 tabs; Duoduo title/host match and food-directory title/path match were both true. This is a real adapter test, **not** a Multica app/Agent end-to-end test.
- Go 1.26.9 focused `-race` tests for `localenv`, `agent`, and `daemon`: passed, using fakes for normal tests. The full `localenv` package test passed.
- The previously failing `TestCodexCancellationDuringInitializeStopsProcessImmediately` passed on a standalone `-race` rerun. The earlier full agent suite failure remains a historical result, not evidence that the full suite now passes.
- `make test` was attempted and stopped before tests: Docker engine unavailable. No existing Multica database or configuration was used.
- Full Multica app/Agent end-to-end, deployed daemon Automation identity, and actual denied-permission dialog were not tested. Denied and exited results were covered with unit tests.

Reproduce isolated checks from `server/` with Go 1.26.9:

```sh
go test -race -p 2 -parallel 2 ./internal/daemon/localenv ./pkg/agent ./internal/daemon -run 'TestChrome|TestProbeCDP|TestParsePsArgs|TestZcode' -count=1
```

The real adapter test is opt-in and needs explicit authorization:

```sh
MULTICA_RUN_REAL_CHROME_READONLY=1 go test -tags=chromeintegration ./internal/daemon/localenv -run '^TestChromeJXARealReadOnly$' -count=1 -v
```

Full repository tests require the repository-managed isolated Docker/PostgreSQL environment and `make test` from the root. Do not point tests at an existing user's database.
