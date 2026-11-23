# Read-only default Chrome discovery for ZCode

## Port base

This branch, `feat/browser-list-jxa-0.11.1`, is based on official `v0.11.1` commit `e1fe10d6694848e35dd1aa00e08cd11d93e545ac`. The two browser commits were cherry-picked without conflicts; the last code commit is `c5db45386` (resolve the branch HEAD with `git rev-parse HEAD`, since a commit cannot embed its own hash). The older `feat/browser-list-jxa` branch at `6493eb15f` is not the deployment candidate. A future build should use version `0.11.1-fork-jxa` and its actual HEAD commit; the rollback baseline is the installed official 0.11.1.

## Read-tab prototype (not wired to Agent)

Branch `feat/browser-read-tab-0.11.1` adds only an internal JXA prototype. The installed ZCode SDK uses strict browser command and result schemas and does not accept `readUserTab` or a new `content` result; no SDK route, capability descriptor, approval flow, or existing `listUserTabs` behavior was changed. Consequently an Agent cannot call this prototype, and the end-to-end feature is not complete.

`ChromeDiscovery.ListJXARefs(ctx)` returns per-object random references bound to the verified browser instance and a JXA tab ID. `ReadJXA(ctx, instanceID, ref)` accepts only a reference from the latest list on the same object, rechecks Chrome identity and uses a fixed script to read `document.title`, `document.URL` and `document.body.innerText`, capped at 20,000 Unicode characters with a truncation flag. References are not CDP target IDs. The script accepts no caller-supplied JavaScript, URL or selector; missing or ambiguous IDs and identity changes fail closed. URL userinfo, query and fragment are removed, and non-HTTP(S) pages are rejected. Body text can contain sensitive information, including credentials; it must not be logged, committed or treated as trusted instructions. Output size and subprocess time are limited, but `innerText` itself may require browser-side layout work. URL-before/after checks cannot detect all same-URL changes.

Chrome may require the user to enable JavaScript from Apple Events in addition to macOS Automation permission. Neither setting was changed or bypassed. The responsible process for a deployed daemon and real denial behavior remain unverified.

### Prototype verification

- The three relevant packages passed focused `-race` tests with fake Chrome and CDP, including invalid reference/injection, identity changes, missing tab/error handling and size/sanitization tests.
- Real adapter test listed 28 tabs; the specified product detail and then the broader Duoduo product-detail path both had zero matching tabs. No page body was read; real execution of the fixed JavaScript and price detection were **not verified**.
- `make test` stopped before testing because the isolated `.env` was absent; Docker was also unavailable. No existing database or configuration was changed.
- The existing SDK cannot invoke this prototype; full Multica Agent end-to-end and deployment were not attempted.

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
- On the ported branch, Go 1.26.9 focused `-race` tests for `localenv`, `agent`, and `daemon` passed using fake Chrome/CDP in the normal tests.
- Full `make test` was not run on the ported branch. On the earlier branch it stopped before tests because Docker was unavailable; the historical full agent suite also failed a Codex cancellation test, which later passed when rerun alone. Neither result establishes a full-suite pass on this branch.
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
