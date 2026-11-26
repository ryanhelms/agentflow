// Package desktophost composes Agentflow-owned MCP connections for Desktop writes.
package desktophost

import (
	"context"
	"errors"

	"github.com/ryanhelms/agentflow/internal/provider/asanahost"
	"github.com/ryanhelms/agentflow/internal/provider/asanaoauth"
	"github.com/ryanhelms/agentflow/internal/provider/native"
	"github.com/ryanhelms/agentflow/internal/provider/rovohost"
)

func NewWriter() *native.SessionWriter {
	rovo := rovohost.NewWriter()
	asana := asanahost.NewWriter(asanahost.Enrollment{
		BoundToken: func(ctx context.Context) (asanahost.Binding, error) {
			b, err := asanaoauth.BoundToken(ctx)
			if errors.Is(err, asanaoauth.ErrNotEnrolled) {
				// This interactive route has no signed-in CLI alternative.
				err = asanaoauth.ErrLoginRequired
			}
			return asanahost.Binding(b), err
		},
		WithBoundToken:   asanaoauth.WithBoundToken,
		ErrNotEnrolled:   asanaoauth.ErrNotEnrolled,
		ErrLoginRequired: asanaoauth.ErrLoginRequired,
		ErrGrantChanged:  asanaoauth.ErrGrantChanged,
		ErrDenied:        asanaoauth.ErrDenied,
		ErrTransport:     asanaoauth.ErrTransport,
	}, asanahost.Scope{EnableCompletion: true})
	return native.NewRoutedSessionWriter("agentflow", map[string]*native.SessionWriter{
		"asana": asana,
		"jira":  rovo, "confluence": rovo,
	})
}
