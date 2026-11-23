package webui

import (
	"context"
	"errors"
	"net/http"

	"github.com/nscaledev/devflow/internal/configconsole"
)

func (s *server) readiness(w http.ResponseWriter, r *http.Request) {
	var input *struct{}
	if !read(w, r, &input) {
		return
	}
	if input == nil {
		problem(w, 400, "expected an empty object")
		return
	}
	out, err := configconsole.Readiness(r.Context(), s.repo.Dir)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		problem(w, 504, "connection checks timed out; check provider connectivity and try again")
	case errors.Is(err, configconsole.ErrCheckoutChanged):
		problem(w, 409, err.Error())
	case err != nil:
		problem(w, 422, err.Error())
	default:
		reply(w, 200, out)
	}
}
