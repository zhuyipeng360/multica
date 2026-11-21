package agent

import (
	"bytes"
	"strings"
	"testing"
)

func TestZcodeBrowserAutomationErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"denied", ErrBrowserAutomationDenied},
		{"exited", ErrBrowserExited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fakeZcodeBrowserProvider{
				browsers: []ZcodeBrowser{{ID: "chrome-1", Generation: 7, Name: "Chrome"}},
				tabsErr:  tc.err,
			}
			var buf bytes.Buffer
			client := &zcodeClient{cfg: Config{ZcodeBrowserProvider: provider}, stdin: &buf}
			out := browserTestRPC(t, client, &buf, "interaction/browserExecute", browserTestExecute())
			browserTestError(t, out, "execution_error")
			if !strings.Contains(buf.String(), tc.err.Error()) {
				t.Fatalf("missing explicit error: %s", buf.String())
			}
		})
	}
}
