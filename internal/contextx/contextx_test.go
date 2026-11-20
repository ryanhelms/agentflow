package contextx

import "testing"

func TestParseCriteria(t *testing.T) {
	desc := "Add a quota API.\n\nAcceptance criteria\n- POST validates limits\n- Quota stored per region\n- 429 returned when quota is exceeded\n\nNotes\n- not a criterion\n"
	items := ParseCriteria(desc)
	if len(items) != 3 || items[2].Text != "429 returned when quota is exceeded" {
		t.Fatalf("items = %+v", items)
	}
	tasks := ParseCriteria("Do it\n[x] first\n[ ] second\n")
	if len(tasks) != 2 || !tasks[0].Done || tasks[1].Done {
		t.Fatalf("tasks = %+v", tasks)
	}
}

func TestSyncKeepsDone(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	c, _ := s.Sync("PLAT-1", []Item{{Text: "A"}, {Text: "B"}})
	c.Items[0].Done, c.Items[0].Evidence = true, "abc123"
	if err := s.Save(c); err != nil {
		t.Fatal(err)
	}
	c2, _ := s.Sync("PLAT-1", []Item{{Text: "a"}, {Text: "B"}, {Text: "C"}})
	if !c2.Items[0].Done || c2.Items[0].Evidence != "abc123" || len(c2.Items) != 3 {
		t.Fatalf("c2 = %+v", c2)
	}
	if d, n := c2.Progress(); d != 1 || n != 3 {
		t.Errorf("progress %d/%d", d, n)
	}
	if c2.Find("c") != 2 {
		t.Error("find failed")
	}
}

func TestScopeCheck(t *testing.T) {
	d := ScopeCheck([]string{"api/quota/a.go", "web/x.ts", "go.sum"}, []string{"api/quota/"}, "+// TODO: handle burst\n-old\n")
	if len(d.Files) != 1 || d.Files[0] != "web/x.ts" || len(d.TODOs) != 1 {
		t.Fatalf("d = %+v", d)
	}
}
