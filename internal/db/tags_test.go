package db

import (
	"reflect"
	"testing"
)

func TestListTags(t *testing.T) {
	database := newTestDB(t)

	must := func(query string) {
		if _, err := database.Exec(query); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}
	must("INSERT INTO works (id, title, sort_title, tags) VALUES ('w1', 'T1', 'T1', ' Fantasy, classics, Fantasy ')")
	must("INSERT INTO works (id, title, sort_title, tags) VALUES ('w2', 'T2', 'T2', 'science fiction, CLASSICS')")
	must("INSERT INTO works (id, title, sort_title, tags) VALUES ('w3', 'T3', 'T3', '')")
	must("INSERT INTO works (id, title, sort_title, tags) VALUES ('w4', 'T4', 'T4', '100% real, under_score')")
	must("INSERT INTO works (id, title, sort_title, tags) VALUES ('w5', 'T5', 'T5', 'Классика')")
	must("INSERT INTO works (id, title, sort_title, tags, deleted_at) VALUES ('w_deleted', 'Deleted', 'Deleted', 'archived', 1)")

	all, err := ListTags(database, FullVisibilityScope(), "", 20)
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	wantAll := []string{"100% real", "classics", "Fantasy", "science fiction", "under_score", "Классика"}
	if !reflect.DeepEqual(all, wantAll) {
		t.Fatalf("ListTags all = %v; want %v", all, wantAll)
	}

	filtered, err := ListTags(database, FullVisibilityScope(), "SCI", 20)
	if err != nil {
		t.Fatalf("ListTags filtered: %v", err)
	}
	if !reflect.DeepEqual(filtered, []string{"science fiction"}) {
		t.Fatalf("ListTags SCI = %v; want [science fiction]", filtered)
	}

	percent, err := ListTags(database, FullVisibilityScope(), "%", 20)
	if err != nil {
		t.Fatalf("ListTags %%: %v", err)
	}
	if !reflect.DeepEqual(percent, []string{"100% real"}) {
		t.Fatalf("ListTags %% = %v; want [100%% real]", percent)
	}

	cyrillic, err := ListTags(database, FullVisibilityScope(), "класс", 20)
	if err != nil {
		t.Fatalf("ListTags cyrillic: %v", err)
	}
	if !reflect.DeepEqual(cyrillic, []string{"Классика"}) {
		t.Fatalf("ListTags cyrillic = %v; want [Классика]", cyrillic)
	}

	limited, err := ListTags(database, FullVisibilityScope(), "", 2)
	if err != nil {
		t.Fatalf("ListTags limited: %v", err)
	}
	if !reflect.DeepEqual(limited, wantAll[:2]) {
		t.Fatalf("ListTags limit = %v; want %v", limited, wantAll[:2])
	}

	must("UPDATE works SET tags = 'newtag' WHERE id = 'w2'")
	updated, err := ListTags(database, FullVisibilityScope(), "new", 20)
	if err != nil {
		t.Fatalf("ListTags updated: %v", err)
	}
	if !reflect.DeepEqual(updated, []string{"newtag"}) {
		t.Fatalf("ListTags after update = %v; want [newtag]", updated)
	}
}
