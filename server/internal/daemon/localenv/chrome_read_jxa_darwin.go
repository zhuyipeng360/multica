//go:build darwin

package localenv

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
)

// Only numeric PID/tab ID and a fixed operation are passed as arguments. The
// page script never reads form element values. innerText may still be sensitive.
const chromeReadJXAScript = `
function run(argv) {
  try {
    const chrome = Application(Number(argv[0]));
    const op = argv[1], wanted = argv[2];
    const tabs = [];
    for (const win of chrome.windows()) {
      for (const tab of win.tabs()) {
        if (op === 'list') tabs.push({id: String(tab.id()), title: tab.title(), url: tab.url()});
        else if (String(tab.id()) === wanted) tabs.push(tab);
      }
    }
    if (op === 'list') return JSON.stringify({status: 'ok', tabs: tabs});
    if (op !== 'read' || tabs.length !== 1) return JSON.stringify({status: 'not_found'});
    const tab = tabs[0], before = tab.url();
    const payload = chrome.execute(tab, {javascript: "(function(){const s=document.body ? document.body.innerText : '';let text='', n=0, truncated=false;for(const c of s){if(n===20000){truncated=true;break;}text+=c;n++;}return JSON.stringify({title:document.title,url:document.URL,text:text,truncated:truncated});})()"});
    const page = JSON.parse(payload), after = tab.url();
    if (before !== after || before !== page.url) return JSON.stringify({status: 'changed'});
    return JSON.stringify({status:'ok', title:page.title, url:page.url, text:page.text, truncated:page.truncated});
  } catch (e) {
    const n = Number(e.errorNumber);
    if (n === -1743) return JSON.stringify({status:'denied'});
    if (n === -600 || n === -609) return JSON.stringify({status:'exited'});
    const msg = String(e.message || '').toLowerCase();
    if (msg.indexOf('allow javascript from apple events') !== -1) return JSON.stringify({status:'js_disabled'});
    return JSON.stringify({status:'unavailable'});
  }
}`

type chromeJXALimitedBuffer struct {
	bytes.Buffer
	max      int
	overflow bool
}

func (b *chromeJXALimitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.max - b.Len()
	if remaining < len(p) {
		b.overflow = true
		if remaining < 0 {
			remaining = 0
		}
		p = p[:remaining]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

func runChromeReadJXA(ctx context.Context, pid int, op, tab string) ([]byte, error) {
	if pid <= 0 || (op != "list" && op != "read") || (op == "read" && !validChromeJXATabID(tab)) || (op == "list" && tab != "") {
		return nil, ErrChromeJXAUnavailable
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-l", "JavaScript", "-e", chromeReadJXAScript, strconv.Itoa(pid), op, tab)
	stdout := &chromeJXALimitedBuffer{max: 1 << 20}
	stderr := &chromeJXALimitedBuffer{max: 4096}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// osascript diagnostics are never returned or logged. Match only numeric
		// Apple Event codes, not arbitrary page text.
		if !stderr.overflow {
			if strings.Contains(stderr.String(), "(-1743)") {
				return nil, ErrChromeAutomationDenied
			}
			if strings.Contains(stderr.String(), "(-600)") || strings.Contains(stderr.String(), "(-609)") {
				return nil, ErrChromeExited
			}
		}
		return nil, ErrChromeJXAUnavailable
	}
	if stdout.overflow || stderr.overflow {
		return nil, ErrChromeJXAUnavailable
	}
	return stdout.Bytes(), nil
}
