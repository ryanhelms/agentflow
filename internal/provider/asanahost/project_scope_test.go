package asanahost

import (
	"errors"
	"testing"

	"github.com/ryanhelms/agentflow/internal/provider"
	"github.com/ryanhelms/agentflow/internal/provider/native"
	"github.com/ryanhelms/agentflow/internal/provider/nativemap"
)

func TestOperationUsesResolvedProject(t *testing.T) {
	const first = "101"
	const second = "102"
	ref := provider.Ref{Instance: "asana", Scope: second, ID: "301"}
	mapped, err := nativemap.AsanaComment(ref, "Approved text")
	if err != nil {
		t.Fatal(err)
	}
	call := mapped.Call()
	op, err := prepareOperation(Scope{}, call)
	if err != nil || op.ref != ref {
		t.Fatalf("prepared %v: %v", op.ref, err)
	}
	if _, err := prepareOperation(Scope{Project: first}, call); !errors.Is(err, native.ErrDenied) {
		t.Fatalf("accepted another project: %v", err)
	}
	call.Scope = ""
	if _, err := prepareOperation(Scope{}, call); !errors.Is(err, native.ErrDenied) {
		t.Fatalf("accepted missing project: %v", err)
	}
}
