package migrations

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// embeddedFiles is the immutable catalog shipped with the application. The
// runner consumes a Catalog rather than reading the filesystem at runtime.
// This keeps migration selection deterministic for a given binary.
//
//go:embed *.up.sql
var embeddedFiles embed.FS

const migrationSuffix = ".up.sql"

var (
	// ErrInvalidCatalog identifies a catalog that cannot be applied safely.
	ErrInvalidCatalog = errors.New("migration: invalid catalog")
)

// Migration is one forward-only schema change and its content identity.
// Name is the embedded filename, including the .up.sql suffix.
type Migration struct {
	Version  int64
	Name     string
	SQL      []byte
	Checksum string
}

// Catalog is an ordered, validated set of forward-only migrations.
type Catalog struct {
	items []Migration
}

// EmbeddedCatalog loads the migrations compiled into this binary.
func EmbeddedCatalog() (Catalog, error) {
	return LoadCatalog(embeddedFiles)
}

// LoadCatalog reads and validates *.up.sql files from a filesystem. Files are
// ordered by their numeric prefix, not by filesystem enumeration order.
func LoadCatalog(files fs.FS) (Catalog, error) {
	if files == nil {
		return Catalog{}, fmt.Errorf("%w: filesystem is required", ErrInvalidCatalog)
	}
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return Catalog{}, fmt.Errorf("%w: cannot read catalog", ErrInvalidCatalog)
	}

	items := make([]Migration, 0, len(entries))
	seenVersions := make(map[int64]struct{}, len(entries))
	seenNames := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, migrationSuffix) {
			if strings.HasSuffix(name, ".down.sql") {
				return Catalog{}, fmt.Errorf("%w: down migration %q is not supported", ErrInvalidCatalog, name)
			}
			continue
		}
		version, err := parseMigrationName(name)
		if err != nil {
			return Catalog{}, err
		}
		if _, exists := seenVersions[version]; exists {
			return Catalog{}, fmt.Errorf("%w: duplicate version %d", ErrInvalidCatalog, version)
		}
		if _, exists := seenNames[name]; exists {
			return Catalog{}, fmt.Errorf("%w: duplicate name %q", ErrInvalidCatalog, name)
		}
		contents, err := fs.ReadFile(files, name)
		if err != nil {
			return Catalog{}, fmt.Errorf("%w: cannot read migration", ErrInvalidCatalog)
		}
		if control := transactionControl(contents); control != "" {
			return Catalog{}, fmt.Errorf("%w: migration contains transaction control %q", ErrInvalidCatalog, control)
		}
		if destructive := destructiveControl(contents); destructive != "" {
			return Catalog{}, fmt.Errorf("%w: destructive statement %q is not supported", ErrInvalidCatalog, destructive)
		}
		digest := sha256.Sum256(contents)
		items = append(items, Migration{
			Version:  version,
			Name:     name,
			SQL:      append([]byte(nil), contents...),
			Checksum: hex.EncodeToString(digest[:]),
		})
		seenVersions[version] = struct{}{}
		seenNames[name] = struct{}{}
	}
	if len(items) == 0 {
		return Catalog{}, fmt.Errorf("%w: no up migrations found", ErrInvalidCatalog)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Version != items[j].Version {
			return items[i].Version < items[j].Version
		}
		return items[i].Name < items[j].Name
	})
	for index, item := range items {
		want := int64(index + 1)
		if item.Version != want {
			return Catalog{}, fmt.Errorf("%w: expected version %d, got %d", ErrInvalidCatalog, want, item.Version)
		}
	}
	return Catalog{items: items}, nil
}

// Validate checks the catalog invariants. Catalogs returned by LoadCatalog
// are already validated; the method is useful at explicit construction
// boundaries and keeps Runner construction fail-fast.
func (c Catalog) Validate() error {
	if len(c.items) == 0 {
		return fmt.Errorf("%w: no migrations", ErrInvalidCatalog)
	}
	for index, item := range c.items {
		if item.Version != int64(index+1) || item.Name == "" || item.Checksum == "" || len(item.SQL) == 0 {
			return fmt.Errorf("%w: migration at index %d is incomplete", ErrInvalidCatalog, index)
		}
		if control := transactionControl(item.SQL); control != "" {
			return fmt.Errorf("%w: migration contains transaction control %q", ErrInvalidCatalog, control)
		}
		if destructive := destructiveControl(item.SQL); destructive != "" {
			return fmt.Errorf("%w: destructive statement %q is not supported", ErrInvalidCatalog, destructive)
		}
		digest := sha256.Sum256(item.SQL)
		if item.Checksum != hex.EncodeToString(digest[:]) {
			return fmt.Errorf("%w: checksum for version %d is not content-derived", ErrInvalidCatalog, item.Version)
		}
	}
	return nil
}

// Migrations returns a defensive copy in deterministic application order.
func (c Catalog) Migrations() []Migration {
	items := make([]Migration, len(c.items))
	for index, item := range c.items {
		items[index] = item
		items[index].SQL = append([]byte(nil), item.SQL...)
	}
	return items
}

// Latest returns the highest version in the catalog.
func (c Catalog) Latest() (Migration, bool) {
	if len(c.items) == 0 {
		return Migration{}, false
	}
	item := c.items[len(c.items)-1]
	item.SQL = append([]byte(nil), item.SQL...)
	return item, true
}

// Lookup returns a migration by its numeric version.
func (c Catalog) Lookup(version int64) (Migration, bool) {
	if version <= 0 || version > int64(len(c.items)) {
		return Migration{}, false
	}
	item := c.items[version-1]
	if item.Version != version {
		return Migration{}, false
	}
	item.SQL = append([]byte(nil), item.SQL...)
	return item, true
}

func parseMigrationName(name string) (int64, error) {
	if name == "" || strings.ContainsAny(name, `/\\`) || !strings.HasSuffix(name, migrationSuffix) {
		return 0, fmt.Errorf("%w: invalid migration name", ErrInvalidCatalog)
	}
	base := strings.TrimSuffix(name, migrationSuffix)
	separator := strings.IndexByte(base, '_')
	if separator <= 0 || separator == len(base)-1 {
		return 0, fmt.Errorf("%w: invalid migration name", ErrInvalidCatalog)
	}
	prefix := base[:separator]
	for _, character := range prefix {
		if character < '0' || character > '9' {
			return 0, fmt.Errorf("%w: invalid migration version", ErrInvalidCatalog)
		}
	}
	version, err := strconv.ParseInt(prefix, 10, 64)
	if err != nil || version <= 0 {
		return 0, fmt.Errorf("%w: invalid migration version", ErrInvalidCatalog)
	}
	for _, character := range base[separator+1:] {
		if character != '_' && character != '-' && !unicode.IsLetter(character) && !unicode.IsDigit(character) {
			return 0, fmt.Errorf("%w: invalid migration name", ErrInvalidCatalog)
		}
	}
	return version, nil
}

// transactionControl finds transaction statements after removing comments and
// quoted literals. Migration SQL is executed inside the runner's transaction;
// allowing a script to control BEGIN/COMMIT would make the history record and
// schema change non-atomic.
func transactionControl(sqlBytes []byte) string {
	cleaned := stripSQLCommentsAndLiterals(string(sqlBytes))
	tokens := strings.FieldsFunc(cleaned, func(character rune) bool {
		return !unicode.IsLetter(character)
	})
	for index, token := range tokens {
		switch strings.ToUpper(token) {
		case "BEGIN", "COMMIT", "ROLLBACK", "SAVEPOINT", "RELEASE":
			return strings.ToUpper(token)
		case "START":
			if index+1 < len(tokens) && strings.EqualFold(tokens[index+1], "TRANSACTION") {
				return "START TRANSACTION"
			}
		}
	}
	return ""
}

func destructiveControl(sqlBytes []byte) string {
	cleaned := stripSQLCommentsAndLiterals(string(sqlBytes))
	tokens := strings.FieldsFunc(cleaned, func(character rune) bool {
		return !unicode.IsLetter(character)
	})
	for _, token := range tokens {
		switch strings.ToUpper(token) {
		case "DROP", "TRUNCATE", "DELETE":
			return strings.ToUpper(token)
		}
	}
	return ""
}

func stripSQLCommentsAndLiterals(sqlText string) string {
	var output strings.Builder
	output.Grow(len(sqlText))
	for index := 0; index < len(sqlText); {
		switch {
		case sqlText[index] == '-' && index+1 < len(sqlText) && sqlText[index+1] == '-':
			index += 2
			for index < len(sqlText) && sqlText[index] != '\n' {
				index++
			}
			output.WriteByte(' ')
		case sqlText[index] == '/' && index+1 < len(sqlText) && sqlText[index+1] == '*':
			index += 2
			for index+1 < len(sqlText) && !(sqlText[index] == '*' && sqlText[index+1] == '/') {
				index++
			}
			if index+1 < len(sqlText) {
				index += 2
			}
			output.WriteByte(' ')
		case sqlText[index] == '\'' || sqlText[index] == '"':
			quote := sqlText[index]
			index++
			for index < len(sqlText) {
				if sqlText[index] == quote {
					if index+1 < len(sqlText) && sqlText[index+1] == quote {
						index += 2
						continue
					}
					index++
					break
				}
				index++
			}
			output.WriteByte(' ')
		default:
			output.WriteByte(sqlText[index])
			index++
		}
	}
	return output.String()
}
