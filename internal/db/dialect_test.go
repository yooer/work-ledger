package db

import "testing"

func TestTranslate(t *testing.T) {
	cases := []struct {
		d       Dialect
		in, out string
	}{
		{SQLite, "SELECT * FROM t WHERE a = ?", "SELECT * FROM t WHERE a = ?"},
		{Postgres, "SELECT * FROM t WHERE a = ? AND b LIKE ? AND c = '?' AND d = ?", "SELECT * FROM t WHERE a = $1 AND b ILIKE $2 AND c = '?' AND d = $3"},
		{Postgres, "SELECT 'it''s LIKE' FROM t WHERE x = ?", "SELECT 'it''s LIKE' FROM t WHERE x = $1"},
		{Postgres, "INSERT OR IGNORE INTO p (a, b) VALUES (?, ?)", "INSERT INTO p (a, b) VALUES ($1, $2) ON CONFLICT DO NOTHING"},
		{MySQL, "INSERT OR IGNORE INTO p (a, b) VALUES (?, ?)", "INSERT IGNORE INTO p (a, b) VALUES (?, ?)"},
		{MySQL, "INSERT INTO s (name, value) VALUES (?, ?)\n\t\tON CONFLICT(name) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at",
			"INSERT INTO s (name, value) VALUES (?, ?)\n\t\tON DUPLICATE KEY UPDATE value = VALUES(value), updated_at = VALUES(updated_at)"},
		{Postgres, "INSERT INTO s (name) VALUES (?) ON CONFLICT(name) DO UPDATE SET value = excluded.value",
			"INSERT INTO s (name) VALUES ($1) ON CONFLICT(name) DO UPDATE SET value = excluded.value"},
	}
	for _, c := range cases {
		if got := translate(c.d, c.in); got != c.out {
			t.Errorf("%s:\n got %q\nwant %q", c.d, got, c.out)
		}
	}
}
