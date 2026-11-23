package slackoauth

import (
	"errors"
	"testing"
)

func TestBoundIdentityTracksStoredConsent(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if _, err := f.e.identity(ctx); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("absent grant: %v", err)
	}
	g := grant{App: f.app, Generation: "first", Team: "T0123456789", User: "U0123456789", Scope: scopes, Access: "fixture-access"}
	for _, generation := range []string{"first", "second"} {
		g.Generation = generation
		if err := f.e.save(g); err != nil {
			t.Fatal(err)
		}
		got, err := f.e.identity(ctx)
		if err != nil || got != (GrantIdentity{Team: g.Team, User: g.User, Generation: generation}) {
			t.Fatalf("identity = %+v, error = %v", got, err)
		}
		g.Team, g.User = "T1123456789", "U1123456789"
	}
}
