package app

import "testing"

func TestDraftSecrets(t *testing.T) {
	// A key shape split so this file holds no secret literal.
	key := "AKIA" + "ABCDEFGHIJKLMNOP"
	diff := "diff --git a/x.env b/x.env\n+++ b/x.env\n+AWS=" + key + "\n-old=" + key + "\n"
	hits := DraftSecrets(diff)
	if len(hits) != 1 || hits[0].File != "x.env" || hits[0].Kind != "AWS access key" {
		t.Fatalf("hits = %+v", hits)
	}
	if len(DraftSecrets("+++ b/a.go\n+fmt.Println(1)\n")) != 0 {
		t.Fatal("a plain line is a hit")
	}
}
