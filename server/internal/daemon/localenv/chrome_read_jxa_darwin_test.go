//go:build darwin

package localenv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func fakeJXAReadDiscovery() *ChromeDiscovery {
	d := fakeChromeDiscovery(1234)
	noCDP(d)
	d.deps.jxaRead = func(_ context.Context, pid int, op, tab string) ([]byte, error) {
		if pid != 123 {
			return nil, errors.New("unexpected pid")
		}
		if op == "list" {
			return []byte(`{"status":"ok","tabs":[{"id":"42","title":"example","url":"https://user:pass@example.test/a?secret=x#fragment"}]}`), nil
		}
		if op == "read" && tab == "42" {
			return []byte(`{"status":"ok","title":"example","url":"https://user:pass@example.test/a?secret=x#fragment","text":"sample","truncated":false}`), nil
		}
		return nil, errors.New("unexpected request")
	}
	return d
}

func TestChromeReadJXAFake(t *testing.T) {
	d := fakeJXAReadDiscovery()
	id, tabs, err := d.ListJXARefs(context.Background())
	if err != nil || len(tabs) != 1 || tabs[0].URL != "https://example.test/a" || !strings.HasPrefix(tabs[0].Ref, "jxa:") {
		t.Fatal("list failed or URL not sanitized", err)
	}
	page, err := d.ReadJXA(context.Background(), id, tabs[0].Ref)
	if err != nil || page.URL != "https://example.test/a" || page.Text != "sample" || page.Truncated {
		t.Fatal("read or sanitization failed", err)
	}
	for _, ref := range []string{"42", "jxa:42", `42;alert(1)`, tabs[0].Ref + `';foo()`} {
		if _, err := d.ReadJXA(context.Background(), id, ref); !errors.Is(err, ErrChromeJXAInvalidRef) {
			t.Fatal("accepted invalid reference")
		}
	}
	if _, err := d.ReadJXA(context.Background(), id+"x", tabs[0].Ref); !errors.Is(err, ErrChromeJXAInvalidRef) {
		t.Fatal("accepted wrong instance")
	}
	other := fakeJXAReadDiscovery()
	if _, err := other.ReadJXA(context.Background(), id, tabs[0].Ref); !errors.Is(err, ErrChromeJXAInvalidRef) {
		t.Fatal("accepted foreign reference")
	}
}

func TestChromeReadJXAProcessChange(t *testing.T) {
	d := fakeJXAReadDiscovery()
	id, tabs, err := d.ListJXARefs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	old := d.deps.jxaRead
	d.deps.jxaRead = func(ctx context.Context, pid int, op, tab string) ([]byte, error) {
		b, e := old(ctx, pid, op, tab)
		editFakeProcess(func(p *chromeProcess) { p.start++ })(&d.deps)
		return b, e
	}
	if _, err := d.ReadJXA(context.Background(), id, tabs[0].Ref); !errors.Is(err, ErrChromeIdentityUnverified) {
		t.Fatal("process change not detected", err)
	}
}

func TestChromeReadJXAJSONAndBounds(t *testing.T) {
	d := fakeJXAReadDiscovery()
	id, tabs, err := d.ListJXARefs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		status string
		want   error
	}{{"denied", ErrChromeAutomationDenied}, {"exited", ErrChromeExited}, {"not_found", ErrChromeJXATabNotFound}, {"js_disabled", ErrChromeJXAJavaScriptDisabled}, {"changed", ErrChromeJXAPageChanged}, {"oops", ErrChromeJXAUnavailable}} {
		d.deps.jxaRead = func(context.Context, int, string, string) ([]byte, error) {
			return []byte(fmt.Sprintf(`{"status":%q}`, tc.status)), nil
		}
		if _, err := d.ReadJXA(context.Background(), id, tabs[0].Ref); !errors.Is(err, tc.want) {
			t.Fatal("wrong status mapping", tc.status, err)
		}
	}
	for _, n := range []int{20000, 20001} {
		payload, _ := json.Marshal(map[string]any{"status": "ok", "title": "x", "url": "https://example.test/?secret=x", "text": strings.Repeat("界", n), "truncated": true})
		d.deps.jxaRead = func(context.Context, int, string, string) ([]byte, error) { return payload, nil }
		page, err := d.ReadJXA(context.Background(), id, tabs[0].Ref)
		if n == 20000 && (err != nil || !page.Truncated || page.URL != "https://example.test/") {
			t.Fatal("boundary failed", err)
		}
		if n == 20001 && !errors.Is(err, ErrChromeJXAUnavailable) {
			t.Fatal("overlong text accepted")
		}
	}
	for _, bad := range []string{`null`, `{"status":"ok"} trailing`, `{"status":"secret-value"}`} {
		if _, err := chromeJXAResultDecode([]byte(bad)); !errors.Is(err, ErrChromeJXAUnavailable) {
			t.Fatal("malformed result accepted")
		}
	}
}

func TestChromeReadJXARejectsDuplicateAndInjectionIDs(t *testing.T) {
	for _, payload := range []string{`{"status":"ok","tabs":[{"id":"1"},{"id":"1"}]}`, `{"status":"ok","tabs":[{"id":"1;alert(1)"}]}`} {
		d := fakeJXAReadDiscovery()
		d.deps.jxaRead = func(context.Context, int, string, string) ([]byte, error) { return []byte(payload), nil }
		if _, _, err := d.ListJXARefs(context.Background()); !errors.Is(err, ErrChromeJXAUnavailable) {
			t.Fatal("accepted invalid tab IDs")
		}
	}
}
