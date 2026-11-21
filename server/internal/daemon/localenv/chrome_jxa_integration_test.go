//go:build darwin && chromeintegration

package localenv

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestChromeJXARealReadOnly(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_CHROME_READONLY") != "1" {
		t.Skip("explicit authorization required")
	}
	id, tabs, err := NewChromeDiscovery().List(context.Background())
	if err != nil {
		t.Fatalf("Chrome read-only adapter failed: %v", err)
	}
	if id == "" {
		t.Fatal("missing Chrome instance identity")
	}
	var duoduo, food bool
	for _, tab := range tabs {
		u, err := url.Parse(tab.URL)
		if err != nil {
			continue
		}
		if strings.Contains(strings.ToLower(u.Hostname()), "duoduo") && strings.Contains(strings.ToLower(tab.Title), "duoduo") {
			duoduo = true
		}
		if u.Hostname() == "192.168.5.200" && u.Port() == "8888" && u.Path == "/hold/中转站/整理完成_食品图片" && strings.Contains(tab.Title, "食品图片") {
			food = true
		}
	}
	t.Logf("tab count=%d; Duoduo title and host match=%t; food title and path match=%t", len(tabs), duoduo, food)
	if !duoduo || !food {
		t.Fatal("required ground-truth tabs were not both found")
	}
}
