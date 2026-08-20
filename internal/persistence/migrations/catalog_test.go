package migrations

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
)

func TestEmbeddedCatalogIsOrderedAndContentAddressed(t *testing.T) {
	catalog, err := EmbeddedCatalog()
	if err != nil {
		t.Fatalf("EmbeddedCatalog() error = %v", err)
	}
	items := catalog.Migrations()
	if len(items) != 4 {
		t.Fatalf("embedded migration count = %d, want 4", len(items))
	}
	for index, item := range items {
		if item.Version != int64(index+1) {
			t.Errorf("migration %q version = %d, want %d", item.Name, item.Version, index+1)
		}
		digest := sha256.Sum256(item.SQL)
		if item.Checksum != hex.EncodeToString(digest[:]) {
			t.Errorf("migration %q checksum is not SHA-256 of SQL", item.Name)
		}
		if transactionControl(item.SQL) != "" {
			t.Errorf("migration %q contains transaction control", item.Name)
		}
	}
	wantNames := []string{
		"0001_canonical_knowledge.up.sql",
		"0002_query_projection.up.sql",
		"0003_textual_projection.up.sql",
		"0004_query_results.up.sql",
	}
	for index, want := range wantNames {
		if items[index].Name != want {
			t.Fatalf("embedded migration name[%d] = %q, want %q", index, items[index].Name, want)
		}
	}
}

func TestLoadCatalogValidatesNamesOrderAndTransactionBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		files     fstest.MapFS
		wantCount int
		wantError bool
	}{
		{
			name:      "sorts numeric versions",
			wantCount: 2,
			files: fstest.MapFS{
				"0002_second.up.sql": &fstest.MapFile{Data: []byte("CREATE TABLE second (id integer);\n")},
				"0001_first.up.sql":  &fstest.MapFile{Data: []byte("CREATE TABLE first (id integer);\n")},
			},
		},
		{
			name: "rejects gap",
			files: fstest.MapFS{
				"0001_first.up.sql": &fstest.MapFile{Data: []byte("CREATE TABLE first (id integer);\n")},
				"0003_third.up.sql": &fstest.MapFile{Data: []byte("CREATE TABLE third (id integer);\n")},
			},
			wantError: true,
		},
		{
			name: "rejects down migration",
			files: fstest.MapFS{
				"0001_first.up.sql":   &fstest.MapFile{Data: []byte("CREATE TABLE first (id integer);\n")},
				"0001_first.down.sql": &fstest.MapFile{Data: []byte("DROP TABLE first;\n")},
			},
			wantError: true,
		},
		{
			name: "rejects begin",
			files: fstest.MapFS{
				"0001_first.up.sql": &fstest.MapFile{Data: []byte("BEGIN; CREATE TABLE first (id integer);\n")},
			},
			wantError: true,
		},
		{
			name: "rejects destructive statement",
			files: fstest.MapFS{
				"0001_first.up.sql": &fstest.MapFile{Data: []byte("DROP TABLE first;\n")},
			},
			wantError: true,
		},
		{
			name: "rejects delete statement",
			files: fstest.MapFS{
				"0001_first.up.sql": &fstest.MapFile{Data: []byte("DELETE FROM first;\n")},
			},
			wantError: true,
		},
		{
			name:      "allows update backfill",
			wantCount: 1,
			files: fstest.MapFS{
				"0001_first.up.sql": &fstest.MapFile{Data: []byte("UPDATE first SET name = 'normalized';\n")},
			},
		},
		{
			name:      "ignores transaction words in comments and literals",
			wantCount: 1,
			files: fstest.MapFS{
				"0001_first.up.sql": &fstest.MapFile{Data: []byte("-- COMMIT\nCREATE TABLE first (message text DEFAULT 'commit');\n")},
			},
		},
		{
			name: "rejects invalid name",
			files: fstest.MapFS{
				"one_first.up.sql": &fstest.MapFile{Data: []byte("CREATE TABLE first (id integer);\n")},
			},
			wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalog, err := LoadCatalog(test.files)
			if test.wantError {
				if err == nil {
					t.Fatal("LoadCatalog() error = nil, want error")
				}
				if !errors.Is(err, ErrInvalidCatalog) {
					t.Fatalf("LoadCatalog() error = %v, want ErrInvalidCatalog", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadCatalog() error = %v", err)
			}
			items := catalog.Migrations()
			if len(items) != test.wantCount {
				t.Fatalf("migration count = %d, want %d", len(items), test.wantCount)
			}
			if items[0].Version != 1 {
				t.Fatalf("first version = %d, want 1", items[0].Version)
			}
			if len(items) > 1 && items[1].Version != 2 {
				t.Fatalf("second version = %d, want 2", items[1].Version)
			}
			items[0].SQL[0] = 'X'
			fresh, ok := catalog.Lookup(1)
			if !ok || fresh.SQL[0] == 'X' {
				t.Fatal("Migrations() did not return a defensive SQL copy")
			}
		})
	}
}

func TestCatalogValidationRejectsChangedContentAndEmptyCatalog(t *testing.T) {
	contents := []byte("CREATE TABLE first (id integer);\n")
	digest := sha256.Sum256(contents)
	catalog := Catalog{items: []Migration{{
		Version:  1,
		Name:     "0001_first.up.sql",
		SQL:      contents,
		Checksum: strings.Repeat("0", sha256.Size*2),
	}}}
	if err := catalog.Validate(); !errors.Is(err, ErrInvalidCatalog) {
		t.Fatalf("Catalog.Validate() error = %v, want ErrInvalidCatalog", err)
	}
	if hex.EncodeToString(digest[:]) == catalog.items[0].Checksum {
		t.Fatal("test checksum unexpectedly matched content")
	}
	if err := (Catalog{}).Validate(); !errors.Is(err, ErrInvalidCatalog) {
		t.Fatalf("empty Catalog.Validate() error = %v, want ErrInvalidCatalog", err)
	}
}
