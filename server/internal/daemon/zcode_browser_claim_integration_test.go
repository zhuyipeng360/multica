//go:build darwin && chromeintegration

package daemon

import (
	"context"
	"net/url"
	"os"
	"os/user"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/localenv"
	"github.com/multica-ai/multica/server/pkg/agent"
)

func TestZcodeChromeClaimSnapshotReal(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_CHROME_READONLY") != "1" {
		t.Skip("explicit authorization required")
	}
	account, err := user.Current()
	if err != nil || account.HomeDir == "" {
		t.Fatal("unable to identify current user home")
	}
	t.Setenv("HOME", account.HomeDir)
	ctx := context.Background()
	p := &zcodeChromeProvider{discovery: localenv.NewChromeDiscovery(), claims: &chromeClaims{listed: make(map[chromeScopeKey]map[string]bool), claimed: make(map[chromeScopeKey]map[string]string)}}
	s := agent.ZcodeBrowserScope{SessionID: "integration-session", WorkspaceKey: "integration", WorkspacePath: "/integration", ClientMode: "desktop-continuous", SessionContext: "live"}
	browsers, err := p.ListBrowsers(ctx, s)
	if err != nil || len(browsers) != 1 {
		t.Fatalf("browser discovery failed: %v", err)
	}
	tabs, err := p.ListUserTabs(ctx, s, browsers[0])
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	var matches []string
	for _, tab := range tabs {
		if tab.URL == nil {
			continue
		}
		u, e := url.Parse(*tab.URL)
		if e == nil && strings.Contains(strings.ToLower(u.Hostname()), "duoduo") && u.Path == "/product-information/catalog/1297" {
			matches = append(matches, tab.ID)
		}
	}
	t.Logf("tabs=%d targetMatches=%d", len(tabs), len(matches))
	if len(matches) != 1 {
		t.Fatal("target missing or ambiguous")
	}
	claim, err := p.ClaimTab(ctx, s, browsers[0], matches[0])
	if err != nil {
		t.Fatalf("claim failed: %v", err)
	}
	t.Logf("claim succeeded; viewport valid=%t", claim.Viewport.Width > 0 && claim.Viewport.Height > 0)
	snapshot, err := p.Snapshot(ctx, s, browsers[0], claim.TabID, agent.ZcodeBrowserSnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if snapshot.DOM == nil {
		t.Fatal("missing semantic DOM")
	}
	var text strings.Builder
	for _, node := range *snapshot.DOM {
		if node.Text != nil {
			text.WriteString(*node.Text)
			text.WriteByte('\n')
		}
	}
	contents := text.String()
	header := strings.Contains(contents, "参考售价") && strings.Contains(contents, "韩元")
	rows := strings.Contains(contents, "FD00060004-S001") && strings.Contains(contents, "FD00060004-S002") && strings.Contains(contents, "FD00060004-S003")
	values := strings.Contains(contents, "11,680") && strings.Contains(contents, "19,560") && strings.Contains(contents, "27,440")
	truncated := snapshot.DOMTruncated != nil && *snapshot.DOMTruncated
	// Reconstruct cells under each DOM row; only compare approved values, never log page text.
	var tableHeader [][]string
	var tableRows [][]string
	var current []string
	rowDepth, cellDepth := -1, -1
	for _, n := range *snapshot.DOM {
		if n.Tag == "tr" {
			if current != nil {
				tableRows = append(tableRows, current)
			}
			current = []string{}
			rowDepth = n.Depth
			cellDepth = -1
		} else if current != nil && n.Depth <= rowDepth {
			tableRows = append(tableRows, current)
			current = nil
			cellDepth = -1
		}
		if current == nil {
			continue
		}
		if n.Tag == "th" || n.Tag == "td" {
			current = append(current, "")
			cellDepth = n.Depth
		}
		if n.Text != nil && cellDepth >= 0 && n.Depth > cellDepth && len(current) > 0 {
			current[len(current)-1] += *n.Text
		}
	}
	if current != nil {
		tableRows = append(tableRows, current)
	}
	for _, row := range tableRows {
		for _, cell := range row {
			if strings.Contains(cell, "参考售价") && strings.Contains(cell, "韩元") {
				tableHeader = append(tableHeader, row)
				break
			}
		}
	}
	association := false
	if len(tableHeader) == 1 {
		col := -1
		for i, cell := range tableHeader[0] {
			if strings.Contains(cell, "参考售价") && strings.Contains(cell, "韩元") {
				col = i
			}
		}
		want := map[string]string{"FD00060004-S001": "11,680", "FD00060004-S002": "19,560", "FD00060004-S003": "27,440"}
		found := map[string]bool{}
		for _, row := range tableRows {
			if col < 0 || col >= len(row) {
				continue
			}
			for sku, price := range want {
				for _, cell := range row {
					if strings.Contains(cell, sku) && strings.TrimSpace(row[col]) == price {
						found[sku] = true
					}
				}
			}
		}
		association = len(found) == 3
	}
	t.Logf("nodes=%d header=%t allSkus=%t allExpectedValues=%t domTruncated=%t structuredHeaderCount=%d rowColumnAssociation=%t", len(*snapshot.DOM), header, rows, values, truncated, len(tableHeader), association)
	if !header || !rows || !values || !association {
		t.Fatal("snapshot insufficient for verified row-to-column pricing")
	}
}
