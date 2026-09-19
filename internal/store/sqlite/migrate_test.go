package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func openRaw(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func appliedVersions(t *testing.T, db *sql.DB) []int {
	t.Helper()
	rows, err := db.Query(`SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var got []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return got
}

// A database that has been through the legacy path is at schema 11 as a fact,
// not an inference — so the baseline is recorded rather than re-executed.
func TestOpenBaselinesTheLegacySchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollops.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	got := appliedVersions(t, s.db)
	if len(got) < legacySchemaVersion {
		t.Fatalf("applied = %v, want at least versions 1..%d", got, legacySchemaVersion)
	}
	for i := 1; i <= legacySchemaVersion; i++ {
		if got[i-1] != i {
			t.Errorf("applied[%d] = %d, want %d", i-1, got[i-1], i)
		}
	}
}

func TestOpeningTwiceChangesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollops.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	before := appliedVersions(t, first.db)
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = second.Close() }()

	after := appliedVersions(t, second.db)
	if len(before) != len(after) {
		t.Errorf("applied went from %v to %v", before, after)
	}
}

func TestAMigrationRunsOnceAndIsRecorded(t *testing.T) {
	db := openRaw(t)
	ctx := context.Background()
	ms := []migration{{
		version: 12,
		name:    "widgets",
		// A second execution would fail on the duplicate table, so running
		// twice without error is itself the assertion that it ran once.
		sql: `CREATE TABLE widgets (id TEXT PRIMARY KEY);`,
	}}

	for range 2 {
		if err := applyVersioned(ctx, db, ms); err != nil {
			t.Fatalf("applyVersioned: %v", err)
		}
	}

	if got := appliedVersions(t, db); len(got) != 1 || got[0] != 12 {
		t.Errorf("applied = %v, want [12]", got)
	}
	if _, err := db.Exec(`INSERT INTO widgets (id) VALUES ('w1')`); err != nil {
		t.Errorf("the migration did not take effect: %v", err)
	}
}

// A half-applied migration is worse than an unapplied one, so each runs in its
// own transaction and a failure leaves no trace of itself.
func TestAFailedMigrationRollsBackAndIsNotRecorded(t *testing.T) {
	db := openRaw(t)
	ctx := context.Background()
	ms := []migration{{
		version: 12,
		name:    "half-broken",
		sql: `CREATE TABLE widgets (id TEXT PRIMARY KEY);
		      CREATE TABLE widgets (id TEXT PRIMARY KEY);`,
	}}

	err := applyVersioned(ctx, db, ms)
	if err == nil {
		t.Fatal("applyVersioned accepted a broken migration")
	}
	if !strings.Contains(err.Error(), "half-broken") {
		t.Errorf("error = %v, want it to name the migration", err)
	}
	if got := appliedVersions(t, db); len(got) != 0 {
		t.Errorf("applied = %v, want none recorded", got)
	}
	var count int
	if err := db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='widgets'`,
	).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 0 {
		t.Error("the first statement survived the rollback")
	}
}

// An out-of-order list would apply a later schema change before the one it
// depends on, so the runner refuses rather than guessing.
func TestMigrationsMustBeOrderedAndUnique(t *testing.T) {
	cases := []struct {
		name string
		ms   []migration
	}{
		{"out of order", []migration{
			{version: 13, name: "b", sql: `CREATE TABLE b (id TEXT);`},
			{version: 12, name: "a", sql: `CREATE TABLE a (id TEXT);`},
		}},
		{"duplicate version", []migration{
			{version: 12, name: "a", sql: `CREATE TABLE a (id TEXT);`},
			{version: 12, name: "b", sql: `CREATE TABLE b (id TEXT);`},
		}},
		{"below the legacy baseline", []migration{
			{version: 11, name: "a", sql: `CREATE TABLE a (id TEXT);`},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := applyVersioned(context.Background(), openRaw(t), c.ms); err == nil {
				t.Error("applyVersioned = nil, want an error")
			}
		})
	}
}

// A database already carrying a later migration must not have an earlier one
// applied behind it — that is a downgrade wearing an upgrade's clothes.
func TestAnAlreadyAppliedVersionIsSkipped(t *testing.T) {
	db := openRaw(t)
	ctx := context.Background()

	if err := applyVersioned(ctx, db, []migration{
		{version: 12, name: "widgets", sql: `CREATE TABLE widgets (id TEXT PRIMARY KEY);`},
	}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := applyVersioned(ctx, db, []migration{
		{version: 12, name: "widgets", sql: `CREATE TABLE widgets (id TEXT PRIMARY KEY);`},
		{version: 13, name: "gadgets", sql: `CREATE TABLE gadgets (id TEXT PRIMARY KEY);`},
	}); err != nil {
		t.Fatalf("second: %v", err)
	}

	got := appliedVersions(t, db)
	if len(got) != 2 || got[0] != 12 || got[1] != 13 {
		t.Errorf("applied = %v, want [12 13]", got)
	}
}

func TestTheDomainMigrationsAreWellFormed(t *testing.T) {
	db := openRaw(t)
	if err := applyVersioned(context.Background(), db, domainMigrations); err != nil {
		t.Fatalf("domain migrations do not apply to an empty database: %v", err)
	}
	got := appliedVersions(t, db)
	if len(got) != len(domainMigrations) {
		t.Fatalf("applied = %v, want %d versions", got, len(domainMigrations))
	}
	for i, m := range domainMigrations {
		if got[i] != m.version {
			t.Errorf("applied[%d] = %d, want %d", i, got[i], m.version)
		}
	}
}

// Spec 36 asks for an upgrade test from the latest released schema. The legacy
// path is that schema, so opening a database is the upgrade: what this asserts
// is that the domain tables arrive on top of it rather than only onto an empty
// file.
func TestOpeningUpgradesTheLegacySchemaToTheDomainModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollops.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	last := domainMigrations[len(domainMigrations)-1].version
	applied := appliedVersions(t, s.db)
	if applied[len(applied)-1] != last {
		t.Errorf("applied = %v, want it to end at %d", applied, last)
	}

	if _, err := s.db.Exec(
		`INSERT INTO projects (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		"prj_1", "checkout", "2026-09-19T00:00:00Z", "2026-09-19T00:00:00Z",
	); err != nil {
		t.Fatalf("the domain schema is not usable: %v", err)
	}

	// An environment may not name a project that does not exist: the foreign
	// keys have to be live, not merely declared.
	if _, err := s.db.Exec(
		`INSERT INTO environments (id, project_id, name, kind) VALUES (?, ?, ?, ?)`,
		"env_1", "prj_missing", "production", "production",
	); err == nil {
		t.Error("an environment was accepted for a project that does not exist")
	}
}

// A database that cannot be written to must fail the open rather than leave a
// half-migrated schema behind an apparently successful start.
func TestMigratingAnUnusableDatabaseFails(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(*sql.DB) error{
		"apply":    func(db *sql.DB) error { return applyVersioned(ctx, db, domainMigrations) },
		"baseline": func(db *sql.DB) error { return baselineLegacySchema(ctx, db) },
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			db := openRaw(t)
			if err := db.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
			if err := run(db); err == nil {
				t.Error("got nil, want an error against a closed database")
			}
		})
	}
}

// Reading the applied set is separate from writing it, and it has to fail
// loudly too — a silently empty set would re-run every migration.
func TestReadingTheAppliedSetFromAnUnusableDatabaseFails(t *testing.T) {
	db := openRaw(t)
	ctx := context.Background()
	if err := ensureMigrationTable(ctx, db); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := appliedSet(ctx, db); err == nil {
		t.Error("appliedSet = nil, want an error")
	}
}

// Baselining runs on every open, so it must tolerate already being recorded.
func TestBaseliningTwiceRecordsOneSetOfVersions(t *testing.T) {
	db := openRaw(t)
	ctx := context.Background()
	for range 2 {
		if err := baselineLegacySchema(ctx, db); err != nil {
			t.Fatalf("baselineLegacySchema: %v", err)
		}
	}
	if got := appliedVersions(t, db); len(got) != legacySchemaVersion {
		t.Errorf("applied = %v, want %d versions", got, legacySchemaVersion)
	}
}
