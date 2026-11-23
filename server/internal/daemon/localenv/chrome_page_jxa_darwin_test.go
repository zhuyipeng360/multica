//go:build darwin

package localenv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

const fakeClaim = `{"status":"ok","url":"https://user:pass@example.test/a?token=secret#fragment","title":"Example","viewport":{"width":1440,"height":900}}`
const fakeSnapshot = `{"status":"ok","url":"https://user:pass@example.test/a?token=secret#fragment","title":"Example","dom":[{"tag":"table","depth":0,"inViewport":true},{"tag":"tr","depth":1,"inViewport":true},{"tag":"td","depth":2,"inViewport":true},{"tag":"#text","depth":3,"inViewport":true,"text":"Visible cell"}],"domTruncated":false}`

func fakePageDiscovery() *ChromeDiscovery {
	d := fakeChromeDiscovery(1234)
	noCDP(d)
	d.deps.jxa = func(context.Context, int) ([]ChromeTab, error) {
		return []ChromeTab{{ID: "42", Title: "Example", URL: "https://example.test/a?token=secret"}}, nil
	}
	d.deps.jxaPage = func(_ context.Context, pid int, op, tab string) ([]byte, error) {
		if pid != 123 || tab != "42" {
			return nil, errors.New("private diagnostic")
		}
		switch op {
		case "claim":
			return []byte(fakeClaim), nil
		case "snapshot":
			return []byte(fakeSnapshot), nil
		}
		return nil, errors.New("private diagnostic")
	}
	return d
}

func TestChromeListWithSourcePreservesList(t *testing.T) {
	t.Run("JXA", func(t *testing.T) {
		d := fakePageDiscovery()
		id, tabs, source, err := d.ListWithSource(context.Background())
		if err != nil || source != ChromeListSourceJXA || id == "" || len(tabs) != 1 || tabs[0].ID != "42" || tabs[0].URL != "https://example.test/a" {
			t.Fatalf("JXA source or tabs incorrect: %v", err)
		}
		otherID, otherTabs, err := d.List(context.Background())
		if err != nil || otherID != id || !reflect.DeepEqual(tabs, otherTabs) {
			t.Fatal("List changed", err)
		}
	})
	t.Run("CDP numeric ID stays CDP", func(t *testing.T) {
		var versions, lists atomic.Int32
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/json/version":
				versions.Add(1)
				fmt.Fprint(w, `{"Protocol-Version":"1.3","webSocketDebuggerUrl":"ws://127.0.0.1/devtools/browser/test"}`)
			case "/json/list":
				lists.Add(1)
				fmt.Fprint(w, `[{"type":"page","id":"42","title":"Example","url":"https://example.test/?secret=x"}]`)
			default:
				t.Error("unexpected request")
			}
		}))
		defer s.Close()
		d := fakeChromeDiscovery(s.Listener.Addr().(*net.TCPAddr).Port)
		d.deps.jxa = func(context.Context, int) ([]ChromeTab, error) { t.Fatal("unexpected JXA list"); return nil, nil }
		_, tabs, source, err := d.ListWithSource(context.Background())
		if err != nil || source != ChromeListSourceCDP || len(tabs) != 1 || tabs[0].ID != "42" || tabs[0].URL != "https://example.test/" || versions.Load() != 1 || lists.Load() != 1 {
			t.Fatalf("incorrect source or extra requests: %v %v versions=%d lists=%d", source, err, versions.Load(), lists.Load())
		}
	})
}

func TestChromeJXAClaimAndSnapshotFake(t *testing.T) {
	d := fakePageDiscovery()
	id, tabs, source, err := d.ListWithSource(context.Background())
	if err != nil || source != ChromeListSourceJXA {
		t.Fatal(err)
	}
	var calls []string
	old := d.deps.jxaPage
	d.deps.jxaPage = func(ctx context.Context, pid int, op, tab string) ([]byte, error) {
		calls = append(calls, op)
		return old(ctx, pid, op, tab)
	}
	claim, err := d.ClaimJXATab(context.Background(), id, tabs[0].ID)
	if err != nil || claim.TabID != "42" || claim.URL != "https://example.test/a" || claim.Viewport != (ChromeViewport{Width: 1440, Height: 900}) {
		t.Fatalf("claim failed: %v", err)
	}
	snap, err := d.SnapshotJXATab(context.Background(), id, tabs[0].ID)
	if err != nil || snap.URL != claim.URL || len(snap.DOM) != 4 || snap.DOM[2].Tag != "td" || snap.DOM[3].Text != "Visible cell" || snap.DOMTruncated || snap.Truncated || snap.Elements == nil || len(snap.Elements) != 0 {
		t.Fatalf("snapshot failed: %v", err)
	}
	if !reflect.DeepEqual(calls, []string{"claim", "snapshot"}) {
		t.Fatalf("unexpected calls: %v", calls)
	}
	for _, node := range snap.DOM {
		if !node.InViewport {
			t.Fatal("viewport flag missing")
		}
	}
	encoded, err := json.Marshal(snap)
	if err != nil || strings.Contains(string(encoded), `"rect"`) || strings.Contains(string(encoded), `"selector"`) || strings.Contains(string(encoded), "secret") {
		t.Fatal("snapshot leaked unsupported data", err)
	}
}

func TestChromeJXAInvalidInputs(t *testing.T) {
	d := fakePageDiscovery()
	id, _, _, err := d.ListWithSource(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	d.deps.jxaPage = func(context.Context, int, string, string) ([]byte, error) {
		t.Fatal("unexpected adapter call")
		return nil, nil
	}
	for _, tab := range []string{"", "0", "-1", "01", "+1", " 42", "42;evil()", "999999999999999999999", "jxa:42"} {
		if _, err := d.ClaimJXATab(context.Background(), id, tab); !errors.Is(err, ErrChromeJXAInvalidTab) {
			t.Errorf("accepted invalid tab %q: %v", tab, err)
		}
	}
	for _, instance := range []string{"", id + "x"} {
		if _, err := d.SnapshotJXATab(context.Background(), instance, "42"); !errors.Is(err, ErrChromeJXAInvalidTab) {
			t.Error("accepted wrong instance", err)
		}
	}
}

func TestChromeJXAProcessAndSignatureRevalidation(t *testing.T) {
	for _, mode := range []string{"process", "lock", "signature"} {
		t.Run(mode, func(t *testing.T) {
			d := fakePageDiscovery()
			id, _, _, err := d.ListWithSource(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			old := d.deps.jxaPage
			d.deps.jxaPage = func(ctx context.Context, pid int, op, tab string) ([]byte, error) {
				result, err := old(ctx, pid, op, tab)
				switch mode {
				case "process":
					editFakeProcess(func(p *chromeProcess) { p.start++ })(&d.deps)
				case "lock":
					d.deps.readlink = func(string) (string, error) { return "test-host-456", nil }
				case "signature":
					d.deps.identity = func(context.Context, chromeProcess) bool { return false }
				}
				return result, err
			}
			claim, err := d.ClaimJXATab(context.Background(), id, "42")
			if !errors.Is(err, ErrChromeIdentityUnverified) || claim != (ChromeJXAClaim{}) {
				t.Fatal("identity change not rejected", err)
			}
		})
	}
}

func TestChromeJXAListSignatureRevalidation(t *testing.T) {
	d := fakePageDiscovery()
	old := d.deps.jxa
	d.deps.jxa = func(ctx context.Context, pid int) ([]ChromeTab, error) {
		tabs, err := old(ctx, pid)
		d.deps.identity = func(context.Context, chromeProcess) bool { return false }
		return tabs, err
	}
	id, tabs, source, err := d.ListWithSource(context.Background())
	if !errors.Is(err, ErrChromeIdentityUnverified) || id != "" || tabs != nil || source != "" {
		t.Fatal("list survived signature change", err)
	}
}

func TestChromeJXAErrorsAndBounds(t *testing.T) {
	d := fakePageDiscovery()
	id, _, _, err := d.ListWithSource(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		status string
		want   error
	}{
		{"denied", ErrChromeAutomationDenied}, {"exited", ErrChromeExited}, {"not_found", ErrChromeJXATabNotFound}, {"js_disabled", ErrChromeJXAJavaScriptDisabled}, {"changed", ErrChromeJXAPageChanged}, {"no_viewport", ErrChromeJXAViewportUnavailable}, {"private secret", ErrChromeJXAUnavailable},
	} {
		d.deps.jxaPage = func(context.Context, int, string, string) ([]byte, error) {
			return []byte(fmt.Sprintf(`{"status":%q}`, tc.status)), nil
		}
		if _, err := d.SnapshotJXATab(context.Background(), id, "42"); !errors.Is(err, tc.want) || strings.Contains(err.Error(), "secret") {
			t.Errorf("bad status mapping %q: %v", tc.status, err)
		}
	}
	for _, payload := range []string{
		`null`, `{"status":"ok"} trailing`, strings.Repeat("x", chromeJXAMaxOutput+1),
		`{"status":"ok","url":"https://example.test/","title":"x","dom":[{"tag":"td","depth":0}],"domTruncated":false}`,
		`{"status":"ok","url":"https://example.test/","title":"x","dom":[{"tag":"td","inViewport":true}],"domTruncated":false}`,
		`{"status":"ok","url":"https://example.test/","title":"x","dom":[{"depth":0,"inViewport":true}],"domTruncated":false}`,
		`{"status":"ok","url":"https://example.test/","title":"x","dom":[{"tag":"td","depth":-1,"inViewport":true}],"domTruncated":false}`,
		`{"status":"ok","url":"file:///secret","title":"x","dom":[],"domTruncated":false}`,
		`{"status":"ok","url":"https://example.test/","title":"x","dom":[],"domTruncated":false,"extra":"secret"}`,
	} {
		d.deps.jxaPage = func(context.Context, int, string, string) ([]byte, error) { return []byte(payload), nil }
		if _, err := d.SnapshotJXATab(context.Background(), id, "42"); !errors.Is(err, ErrChromeJXAUnavailable) {
			t.Fatal("accepted bad snapshot", err)
		}
	}
	for _, vp := range []string{`null`, `{"width":0,"height":10}`, `{"width":10,"height":-1}`, `{"width":100001,"height":10}`} {
		d.deps.jxaPage = func(context.Context, int, string, string) ([]byte, error) {
			return []byte(`{"status":"ok","url":"https://example.test/","title":"x","viewport":` + vp + `}`), nil
		}
		if _, err := d.ClaimJXATab(context.Background(), id, "42"); !errors.Is(err, ErrChromeJXAViewportUnavailable) {
			t.Error("accepted invalid viewport", err)
		}
	}
	d.deps.jxaPage = func(context.Context, int, string, string) ([]byte, error) {
		return nil, errors.New("secret internal error")
	}
	if _, err := d.ClaimJXATab(context.Background(), id, "42"); !errors.Is(err, ErrChromeJXAUnavailable) || strings.Contains(err.Error(), "secret") {
		t.Fatal("internal error leaked", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.ClaimJXATab(ctx, id, "42"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
}

func TestChromeJXAScriptSafetyAndBuffer(t *testing.T) {
	for _, needle := range []string{"Application(Number(argv[0]))", "matches.length !== 1", "String(tabs[i].id()) === wanted", "if (op === 'claim')", "target.win.activeTabIndex = target.index", "chrome.activate()", "before !== after", "before !== page.url"} {
		if !strings.Contains(chromePageJXAScript, needle) {
			t.Fatalf("missing script safeguard %q", needle)
		}
	}
	if strings.Count(chromePageJXAScript, "chrome.activate()") != 1 || strings.Contains(chromeSnapshotJS, ".value") || !strings.Contains(chromeSnapshotJS, "'input','textarea'") || !strings.Contains(chromeSnapshotJS, "isContentEditable") {
		t.Fatal("unsafe activation or form extraction")
	}
	for _, tag := range []string{"'table'", "'tr'", "'th'", "'td'"} {
		if !strings.Contains(chromeSnapshotJS, tag) {
			t.Fatal("missing semantic table tag", tag)
		}
	}
	b := &chromeJXALimitedBuffer{max: 3}
	if n, err := b.Write([]byte("secret")); n != 6 || err != nil || !b.overflow || b.String() != "sec" {
		t.Fatal("buffer not bounded")
	}
	if _, err := runChromePageJXA(context.Background(), -1, "claim", "42"); !errors.Is(err, ErrChromeJXAInvalidTab) {
		t.Fatal("invalid pid accepted")
	}
	if _, err := runChromePageJXA(context.Background(), os.Getpid(), "eval", "42"); !errors.Is(err, ErrChromeJXAInvalidTab) {
		t.Fatal("invalid operation accepted")
	}
}
