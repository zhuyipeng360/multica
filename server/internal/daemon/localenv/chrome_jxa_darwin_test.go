//go:build darwin

package localenv

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
)

func noCDP(d *ChromeDiscovery) {
	editFakeProcess(func(p *chromeProcess) { p.args = []string{p.exe} })(&d.deps)
}

func TestChromeJXAFallback(t *testing.T) {
	d := fakeChromeDiscovery(1234)
	noCDP(d)
	calls := 0
	d.deps.jxa = func(_ context.Context, pid int) ([]ChromeTab, error) {
		calls++
		if pid != 123 {
			t.Fatalf("unexpected pid %d", pid)
		}
		return []ChromeTab{{ID: "9", Title: "Example", URL: "https://user:pass@example.test/a?token=secret#fragment"}}, nil
	}
	id, err := d.discover(context.Background())
	if err != nil || id == "" || calls != 0 {
		t.Fatalf("discover: id=%q err=%v calls=%d", id, err, calls)
	}
	gotID, tabs, err := d.list(context.Background())
	want := []ChromeTab{{ID: "9", Title: "Example", URL: "https://example.test/a"}}
	if err != nil || gotID != id || calls != 1 || !reflect.DeepEqual(tabs, want) {
		t.Fatalf("list: id=%q tabs=%#v err=%v calls=%d", gotID, tabs, err, calls)
	}
}

func TestChromeJXADeniedAndIdentityChange(t *testing.T) {
	d := fakeChromeDiscovery(1234)
	noCDP(d)
	d.deps.jxa = func(context.Context, int) ([]ChromeTab, error) { return nil, ErrChromeAutomationDenied }
	if _, _, err := d.list(context.Background()); !errors.Is(err, ErrChromeAutomationDenied) {
		t.Fatalf("denial = %v", err)
	}
	d.deps.jxa = func(context.Context, int) ([]ChromeTab, error) {
		d.deps.readlink = func(string) (string, error) { return "test-host-456", nil }
		return []ChromeTab{{ID: "1"}}, nil
	}
	if _, tabs, err := d.list(context.Background()); !errors.Is(err, ErrChromeIdentityUnverified) || tabs != nil {
		t.Fatalf("changed lock = %#v, %v", tabs, err)
	}
	d.deps.readlink = func(string) (string, error) { return "", os.ErrNotExist }
	if _, _, err := d.list(context.Background()); !errors.Is(err, ErrChromeNotRunning) {
		t.Fatalf("missing lock = %v", err)
	}
}

func TestChromeJXAParsingAndErrors(t *testing.T) {
	tabs, err := parseChromeJXATabs([]byte(`[{"id":"123","title":"Tab","url":"https://user:pass@example.test/a?x=y#z"},{"id":"124","title":"Local","url":"file:///private"}]`))
	want := []ChromeTab{{ID: "123", Title: "Tab", URL: "https://example.test/a"}, {ID: "124", Title: "Local"}}
	if err != nil || !reflect.DeepEqual(tabs, want) {
		t.Fatalf("parsed tabs = %#v, %v", tabs, err)
	}
	for _, raw := range []string{`null`, `{}`, `[{"id":"1","title":null,"url":""}]`, `[{"id":"","title":"x","url":""}]`, `[{"id":"1","title":"x","url":""},{"id":"1","title":"y","url":""}]`, `[] trailing`} {
		if _, err := parseChromeJXATabs([]byte(raw)); !errors.Is(err, ErrChromeJXAUnavailable) {
			t.Errorf("accepted invalid JSON %q: %v", raw, err)
		}
	}
	if !errors.Is(chromeJXAError("execution error: not authorized (-1743)"), ErrChromeAutomationDenied) ||
		!errors.Is(chromeJXAError("execution error (-600)"), ErrChromeExited) ||
		!errors.Is(chromeJXAError("other"), ErrChromeJXAUnavailable) {
		t.Fatal("JXA error mapping failed")
	}
}

func TestChromeUnusableCDPFallsBackToJXA(t *testing.T) {
	d := fakeChromeDiscovery(1234)
	d.deps.listener = func(context.Context, int, int) bool { return false }
	d.deps.jxa = func(context.Context, int) ([]ChromeTab, error) {
		return []ChromeTab{{ID: "1", Title: "Example", URL: "https://example.test/?secret=value"}}, nil
	}
	_, tabs, err := d.list(context.Background())
	if err != nil || len(tabs) != 1 || tabs[0].URL != "https://example.test/" {
		t.Fatalf("fallback = %#v, %v", tabs, err)
	}
}

func TestChromeCDPPreferredOverJXA(t *testing.T) {
	_, d := testCDP(t, `[]`)
	d.deps.jxa = func(context.Context, int) ([]ChromeTab, error) {
		t.Fatal("JXA called despite available CDP")
		return nil, nil
	}
	if _, _, err := d.list(context.Background()); err != nil {
		t.Fatal(err)
	}
}
