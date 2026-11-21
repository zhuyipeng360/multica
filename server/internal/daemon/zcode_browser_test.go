package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/localenv"
	"github.com/multica-ai/multica/server/pkg/agent"
)

type fakeChromeDiscovery struct {
	id      string
	tabs    []localenv.ChromeTab
	err     error
	listID  string
	listErr error
}

func (f fakeChromeDiscovery) Discover(context.Context) (string, error) { return f.id, f.err }
func (f fakeChromeDiscovery) List(context.Context) (string, []localenv.ChromeTab, error) {
	return f.listID, f.tabs, f.listErr
}

func TestZcodeChromeProvider(t *testing.T) {
	ctx := context.Background()
	scope := agent.ZcodeBrowserScope{}
	p := zcodeChromeProvider{discovery: fakeChromeDiscovery{
		id: "chrome-one", listID: "chrome-one",
		tabs: []localenv.ChromeTab{{ID: "tab-one", Title: "Example", URL: "https://example.test/path"}},
	}}
	browsers, err := p.ListBrowsers(ctx, scope)
	if err != nil || len(browsers) != 1 || browsers[0].ID != "chrome-one" {
		t.Fatalf("discover: %#v, %v", browsers, err)
	}
	tabs, err := p.ListUserTabs(ctx, scope, browsers[0])
	if err != nil || len(tabs) != 1 || tabs[0].ID != "tab-one" || *tabs[0].Title != "Example" || *tabs[0].URL != "https://example.test/path" {
		t.Fatalf("list: %#v, %v", tabs, err)
	}
	p.discovery = fakeChromeDiscovery{id: "chrome-one", listID: "chrome-two"}
	if _, err := p.ListUserTabs(ctx, scope, browsers[0]); err == nil {
		t.Fatal("accepted tabs from a different Chrome instance")
	}
	p.discovery = fakeChromeDiscovery{err: localenv.ErrChromeNotRunning}
	if browsers, err := p.ListBrowsers(ctx, scope); err != nil || len(browsers) != 0 {
		t.Fatalf("no Chrome: %#v, %v", browsers, err)
	}
	p.discovery = fakeChromeDiscovery{err: localenv.ErrChromeCDPUnavailable}
	if _, err := p.ListBrowsers(ctx, scope); !errors.Is(err, localenv.ErrChromeCDPUnavailable) {
		t.Fatalf("unavailable CDP: %v", err)
	}
	for _, tc := range []struct{ local, protocol error }{
		{localenv.ErrChromeAutomationDenied, agent.ErrBrowserAutomationDenied},
		{localenv.ErrChromeExited, agent.ErrBrowserExited},
	} {
		p.discovery = fakeChromeDiscovery{listErr: tc.local}
		if _, err := p.ListUserTabs(ctx, scope, browsers[0]); !errors.Is(err, tc.protocol) {
			t.Fatalf("error mapping: %v", err)
		}
	}
}
