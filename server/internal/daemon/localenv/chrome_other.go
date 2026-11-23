//go:build !darwin

package localenv

import "context"

const chromeSupported = false

func chromeProcessInfo(context.Context, int) (chromeProcess, error) {
	return chromeProcess{}, ErrUnsupported
}

func verifyChromeSignature(context.Context, chromeProcess) bool { return false }

func chromeOwnsListener(context.Context, int, int) bool { return false }

func listChromeTabsJXA(context.Context, int) ([]ChromeTab, error) { return nil, ErrUnsupported }

func runChromePageJXA(context.Context, int, string, string) ([]byte, error) {
	return nil, ErrUnsupported
}
