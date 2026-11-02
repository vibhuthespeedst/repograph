// Package store keeps orders.
package store

const insertSQL = "INSERT INTO orders (id) VALUES ($1)"

// DB is the storage implementation.
type DB struct{}

// Get loads one order.
func (d *DB) Get(id string) string {
	return query("SELECT id FROM orders WHERE id = $1", id)
}

// Save stores one order and records it.
func (d *DB) Save(id string) error {
	if !Validate("id", id) {
		return nil
	}
	d.audit(id)
	exec(insertSQL, id)
	return nil
}

func (d *DB) audit(id string) { exec("INSERT INTO audit_log (id) VALUES ($1)", id) }

func query(q, arg string) string { return q + arg }

func exec(q, arg string) { _ = q + arg }

// validators is only reachable through this package-level table.
var validators = map[string]func(string) bool{
	"id": isID,
}

var unrelated = 3

// Validate runs the named check.
func Validate(kind, v string) bool {
	return validators[kind](v) && unrelated > 0
}

func isID(v string) bool { return v != "" }

// Map is generic so instantiated calls must resolve to one function.
func Map[T any](xs []T, f func(T) T) []T {
	for i := range xs {
		xs[i] = f(xs[i])
	}
	return xs
}

func upper(xs []string) []string {
	return Map(xs, func(s string) string { return s })
}

func unused() { _ = upper(nil) }
