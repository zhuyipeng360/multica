//go:build !darwin

package localenv

import "context"

func runChromeReadJXA(context.Context, int, string, string) ([]byte, error) {
	return nil, ErrUnsupported
}
