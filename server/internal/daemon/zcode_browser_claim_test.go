package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/localenv"
	"github.com/multica-ai/multica/server/pkg/agent"
)

type fakeClaimDiscovery struct {
	fakeChromeDiscovery
	source            localenv.ChromeListSource
	claims, snapshots int
}

func (f *fakeClaimDiscovery) ListWithSource(context.Context) (string, []localenv.ChromeTab, localenv.ChromeListSource, error) {
	return f.listID, f.tabs, f.source, f.listErr
}
func (f *fakeClaimDiscovery) ClaimJXATab(_ context.Context, id, tab string) (localenv.ChromeJXAClaim, error) {
	f.claims++
	if id != "chrome-one" || tab != "101" {
		return localenv.ChromeJXAClaim{}, errors.New("unexpected tab")
	}
	return localenv.ChromeJXAClaim{TabID: tab, URL: "https://example.test/", Title: "Example", Viewport: localenv.ChromeViewport{Width: 100, Height: 100}}, nil
}
func (f *fakeClaimDiscovery) SnapshotJXATab(_ context.Context, id, tab string) (localenv.ChromeJXASnapshot, error) {
	f.snapshots++
	return localenv.ChromeJXASnapshot{URL: "https://example.test/", Title: "Example", DOM: []localenv.ChromeDOMNode{{Tag: "td", Depth: 0, InViewport: true, Text: "sample"}}, Elements: []struct{}{}}, nil
}

func TestZcodeChromeClaimScope(t *testing.T) {
	ctx := context.Background()
	f := &fakeClaimDiscovery{fakeChromeDiscovery: fakeChromeDiscovery{id: "chrome-one", listID: "chrome-one", tabs: []localenv.ChromeTab{{ID: "101", Title: "Example", URL: "https://example.test/"}}}, source: localenv.ChromeListSourceJXA}
	p := &zcodeChromeProvider{discovery: f, claims: &chromeClaims{listed: make(map[chromeScopeKey]map[string]bool), claimed: make(map[chromeScopeKey]map[string]string)}}
	s := agent.ZcodeBrowserScope{SessionID: "session-a", WorkspaceKey: "wk", WorkspacePath: "/wk", ClientMode: "desktop-continuous", SessionContext: "live"}
	b := agent.ZcodeBrowser{ID: "chrome-one", Name: "Chrome"}
	if _, err := p.ClaimTab(ctx, s, b, "101"); err == nil {
		t.Fatal("unlisted tab accepted")
	}
	if _, err := p.ListUserTabs(ctx, s, b); err != nil {
		t.Fatal(err)
	}
	other := s
	other.SessionID = "session-b"
	if _, err := p.ClaimTab(ctx, other, b, "101"); err == nil {
		t.Fatal("cross-session claim accepted")
	}
	claim, err := p.ClaimTab(ctx, s, b, "101")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Snapshot(ctx, other, b, claim.TabID, agent.ZcodeBrowserSnapshotOptions{}); err == nil {
		t.Fatal("cross-session snapshot accepted")
	}
	snap, err := p.Snapshot(ctx, s, b, claim.TabID, agent.ZcodeBrowserSnapshotOptions{})
	if err != nil || snap.DOM == nil || len(*snap.DOM) != 1 {
		t.Fatal("snapshot failed", err)
	}
	f.source = localenv.ChromeListSourceCDP
	if _, err := p.ListUserTabs(ctx, s, b); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ClaimTab(ctx, s, b, "101"); err == nil {
		t.Fatal("CDP ID accepted as JXA")
	}
}
