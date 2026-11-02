//go:build extra

package store

// Extra only builds with the extra tag.
func Extra() bool { return isID("x") }
