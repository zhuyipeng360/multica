# Default Chrome claim and snapshot for ZCode

## Scope and protocol

This branch `feat/browser-claim-snapshot-0.11.1` starts at deployed `56daf2b8f2fbac8e694026b8ccc183b3dbf813da`. It adds `claimTab` and `snapshot` beside `listUserTabs`; it does not change `interaction/requestPermission` or reverse RPC routes. Arbitrary `evaluate`, navigation, clicking, filling, other `playwright` actions, `list` and `activateTab` remain rejected. `includeHidden=true` is rejected.

The [ZCode browser SDK](https://github.com/zai-org/ZCode/blob/fec975905a2abd4c07e35c40d51fa097a2258ec9/packages/shared/src/browser-use/commands.ts) can perform `browser.user.openTabs()` → `browser.user.claimTab(info)` → the returned `tab.dom_cua.get_visible_dom()` within one execution; the last call maps to `snapshot`. Claim returns the required controlled `tabId`, title, sanitized URL, and actual positive viewport. Snapshot returns `{url,title,dom,domTruncated,elements:[],truncated}`; DOM nodes contain actual visible tag, depth, viewport intersection and text. It is a bounded semantic DOM representation, **not a full AX/ARIA tree**; it does not fabricate interactive element references, selectors, rectangles or viewport. The descriptor enables only `BrowserUser.claimTab`, `Tab.dom_cua` and `DomCUAAPI.get_visible_dom` in addition to existing `BrowserUser.openTabs`.

The current browser-use skill's ordinary cross-call workflow expects `tabs.list()` and `tabs.get()`, which this branch does not support; the one-call chain above has not been verified in the actual Multica Agent. Default `playwright.domSnapshot()` is also not supported. No claim is made that full app/SDK end-to-end is complete.

## Identity, authorization and data

The default Chrome `SingletonLock`, same user, process identity/start time/parent, Google signature and arguments are verified before and after access. Existing list behavior remains CDP-first; `ListWithSource` reports the actual ID namespace. Only IDs actually returned by a JXA-sourced list in the same browser instance and session/workspace scope may be claimed; CDP IDs are rejected rather than guessed. Claim returns a random controlled handle bound to that scope and instance; snapshot rejects unclaimed or cross-scope handles. Only claim activates the target once. Activation may cause page focus/visibility events or site-side effects; snapshot does not activate.

The adapter uses `/usr/bin/osascript` with fixed arguments and 30-second timeout. Two fixed page scripts read viewport/title/URL, and visible semantic DOM respectively; no caller-provided script, selectors or URLs are executed. Input fields, editable content and hidden nodes are excluded; iframes/shadow content and offscreen nodes may be absent. The snapshot is capped at 500 nodes and 20,000 characters, with a truncation marker. URL userinfo, query and fragment are removed; title, path and visible text may still be sensitive, including credentials, and are not logged or committed. URL equality cannot detect same-URL content changes.

macOS Automation may prompt for the responsible daemon/app/host process; its exact deployed TCC attribution has not been verified. The user must approve or refuse in **System Settings → Privacy & Security → Automation**; never bypass or click on the user's behalf. Chrome also requires its user-controlled “Allow JavaScript from Apple Events” setting. `-1743` and `-600/-609` are mapped to explicit safe errors; other errors are not logged verbatim.

## Files and verification

- `server/pkg/agent/zcode_browser.go` and tests: strict command dispatch, descriptor, schema-compatible responses, error handling and rejection of unsupported commands.
- `server/internal/daemon/zcode_browser*.go` and tests: per-scope list/claim ownership and provider mapping.
- `server/internal/daemon/localenv/chrome*.go` and tests: source-aware discovery, PID-targeted JXA claim and bounded visible DOM snapshot.

Go 1.26.9 focused `-race` tests on `localenv`, `agent`, and `daemon` passed, including unsupported/evaluate, scoped claim and identity tests. Full `make test` stopped before tests because the isolated `.env` was missing; Docker was also unavailable. No existing Multica database or configuration was used.

The real, opt-in adapter test found exactly one target among 29 tabs, claimed it, received a real viewport, and produced 437 visible DOM nodes without truncation. It located one reference-price header and verified the three SKU/column associations against the approved values; no page text or URL was printed. The first attempt used the daemon test's isolated HOME and did not find Chrome; the explicit integration test then selected the current account's real HOME. This validates the adapter chain, **not** full Multica GUI/Agent execution or deployment authorization. No currently running application binary was replaced.

Reproduce fake checks from `server/`:

```sh
go test -race -p 2 -parallel 2 ./internal/daemon/localenv ./pkg/agent ./internal/daemon -run 'TestChrome|TestProbeCDP|TestParsePsArgs|TestZcode' -count=1
```

Real Chrome integration is opt-in (`chromeintegration`, `MULTICA_RUN_REAL_CHROME_READONLY=1`) and activates the specified target; obtain explicit user authorization before running. Full repository tests require the repository-managed isolated Docker/PostgreSQL setup.
