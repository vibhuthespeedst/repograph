package store

import "testing"

func TestGet(t *testing.T) {
	if (&DB{}).Get("x") == "" {
		t.Fatal("empty")
	}
}

func helper() bool { return isID("x") }

func TestIsID(t *testing.T) {
	if !helper() {
		t.Fatal("rejected")
	}
}
