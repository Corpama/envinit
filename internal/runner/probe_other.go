//go:build !linux

package runner

import "context"

func (a *App) probeNICBindings(_ context.Context, bindings []interfaceBinding) ([]interfaceBinding, error) {
	return bindings, nil
}
