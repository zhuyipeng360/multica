//go:build darwin

package localenv

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
)

// This fixed page program reads no input values or editable content. Visible
// text can still contain sensitive data. It is DOM data, not an AX tree.
const chromeSnapshotJS = `(function() {
  const width = window.innerWidth, height = window.innerHeight;
  if (!Number.isInteger(width) || !Number.isInteger(height) || width <= 0 || height <= 0 || width > 100000 || height > 100000) return JSON.stringify({status:'no_viewport'});
  const result = {status:'ok', url:document.URL, title:document.title, dom:[], domTruncated:false};
  const tags = new Set(['main','header','footer','nav','article','section','aside','h1','h2','h3','h4','h5','h6','p','div','span','a','button','ul','ol','li','dl','dt','dd','table','caption','thead','tbody','tfoot','tr','th','td','blockquote','pre','code','label']);
  const skip = new Set(['script','style','noscript','template','input','textarea','select','option','iframe','svg','canvas']);
  const stack = document.body ? [{node:document.body, depth:0}] : [];
  let visited = 0, chars = 0;
  function intersects(r) { return r.width > 0 && r.height > 0 && r.right > 0 && r.bottom > 0 && r.left < width && r.top < height; }
  while (stack.length) {
    if (++visited > 10000 || result.dom.length >= 500 || chars >= 20000) { result.domTruncated = true; break; }
    const item = stack.pop(), node = item.node;
    if (item.depth > 64) { result.domTruncated = true; continue; }
    if (node.nodeType === 3) {
      const text = (node.nodeValue || '').replace(/\s+/g, ' ').trim();
      if (!text) continue;
      const range = document.createRange(); range.selectNodeContents(node);
      if (!intersects(range.getBoundingClientRect())) continue;
      const limit = Math.min(256, 20000 - chars);
      let value = '', count = 0;
      for (const character of text) {
        if (count === limit) { result.domTruncated = true; break; }
        value += character; count++;
      }
      chars += count;
      result.dom.push({tag:'#text', depth:item.depth, inViewport:true, text:value});
      continue;
    }
    if (node.nodeType !== 1) continue;
    const tag = node.tagName.toLowerCase();
    if (skip.has(tag) || node.isContentEditable || node.hasAttribute('hidden') || node.getAttribute('aria-hidden') === 'true') continue;
    const style = getComputedStyle(node);
    if (style.display === 'none' || style.visibility === 'hidden' || style.visibility === 'collapse' || Number(style.opacity) === 0) continue;
    const include = tags.has(tag) && intersects(node.getBoundingClientRect());
    if (include) result.dom.push({tag:tag, depth:item.depth, inViewport:true});
    const depth = item.depth + (include ? 1 : 0);
    for (let child = node.lastChild; child; child = child.previousSibling) {
      if (stack.length >= 10000) { result.domTruncated = true; break; }
      stack.push({node:child, depth:depth});
    }
  }
  return JSON.stringify(result);
})()`

const chromeClaimJS = `(function() {
  const width = window.innerWidth, height = window.innerHeight;
  if (!Number.isInteger(width) || !Number.isInteger(height) || width <= 0 || height <= 0 || width > 100000 || height > 100000) return JSON.stringify({status:'no_viewport'});
  return JSON.stringify({status:'ok', url:document.URL, title:document.title, viewport:{width:width,height:height}});
})()`

// Application is addressed by verified PID. Only fixed op, numeric PID and
// numeric tab ID cross the argument boundary. The tab must be unique across
// windows. URL comparison catches navigation without returning raw URLs.
var chromePageJXAScript = `
function run(argv) {
  try {
    const chrome = Application(Number(argv[0])), op = argv[1], wanted = argv[2];
    const matches = [];
    for (const win of chrome.windows()) {
      const tabs = win.tabs();
      for (let i = 0; i < tabs.length; i++) {
        if (String(tabs[i].id()) === wanted) matches.push({win:win, tab:tabs[i], index:i+1});
      }
    }
    if ((op !== 'claim' && op !== 'snapshot') || matches.length !== 1) return JSON.stringify({status:'not_found'});
    const target = matches[0], tab = target.tab;
    if (op === 'claim') {
      if (String(target.win.tabs()[target.index-1].id()) !== wanted) return JSON.stringify({status:'changed'});
      target.win.activeTabIndex = target.index;
      target.win.index = 1;
      chrome.activate();
      if (String(target.win.activeTab().id()) !== wanted) return JSON.stringify({status:'changed'});
    }
    const before = tab.url();
    const payload = chrome.execute(tab, {javascript:op === 'claim' ? ` + strconv.Quote(chromeClaimJS) + ` : ` + strconv.Quote(chromeSnapshotJS) + `});
    const page = JSON.parse(payload), after = tab.url();
    if (page.status === 'no_viewport') return JSON.stringify({status:'no_viewport'});
    if (before !== after || before !== page.url || String(tab.id()) !== wanted) return JSON.stringify({status:'changed'});
    return JSON.stringify(page);
  } catch (e) {
    const n = Number(e.errorNumber);
    if (n === -1743) return JSON.stringify({status:'denied'});
    if (n === -600 || n === -609) return JSON.stringify({status:'exited'});
    if (String(e.message || '').toLowerCase().indexOf('allow javascript from apple events') !== -1) return JSON.stringify({status:'js_disabled'});
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

func runChromePageJXA(ctx context.Context, pid int, op, tab string) ([]byte, error) {
	if pid <= 0 || (op != "claim" && op != "snapshot") || !validChromeJXATabID(tab) {
		return nil, ErrChromeJXAInvalidTab
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-l", "JavaScript", "-e", chromePageJXAScript, strconv.Itoa(pid), op, tab)
	stdout := &chromeJXALimitedBuffer{max: chromeJXAMaxOutput}
	stderr := &chromeJXALimitedBuffer{max: 4096}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
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
