//go:build darwin

package localenv

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const chromeListJXA = `
function run(argv) {
  const chrome = Application(Number(argv[0]));
  const result = [];
  for (const window of chrome.windows()) {
    for (const tab of window.tabs()) {
      result.push({id: String(tab.id()), title: tab.title(), url: tab.url()});
    }
  }
  return JSON.stringify(result);
}`

func listChromeTabsJXA(ctx context.Context, pid int) ([]ChromeTab, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-l", "JavaScript", "-e", chromeListJXA, strconv.Itoa(pid))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, chromeJXAError(stderr.String())
	}
	return parseChromeJXATabs(stdout.Bytes())
}

func chromeJXAError(stderr string) error {
	switch {
	case strings.Contains(stderr, "-1743"):
		return ErrChromeAutomationDenied
	case strings.Contains(stderr, "-600"):
		return ErrChromeExited
	default:
		return ErrChromeJXAUnavailable
	}
}

func parseChromeJXATabs(data []byte) ([]ChromeTab, error) {
	var rows []struct {
		ID    *string `json:"id"`
		Title *string `json:"title"`
		URL   *string `json:"url"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&rows) != nil || rows == nil || decoder.Decode(new(any)) != io.EOF {
		return nil, ErrChromeJXAUnavailable
	}
	tabs := make([]ChromeTab, 0, len(rows))
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row.ID == nil || *row.ID == "" || row.Title == nil || row.URL == nil || seen[*row.ID] {
			return nil, ErrChromeJXAUnavailable
		}
		seen[*row.ID] = true
		tabs = append(tabs, ChromeTab{ID: *row.ID, Title: *row.Title, URL: safeChromeURL(*row.URL)})
	}
	return tabs, nil
}
