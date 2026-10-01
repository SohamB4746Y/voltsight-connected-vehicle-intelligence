package main

import (
	"reflect"
	"testing"
)

// The CORS allow-list is exactly what is configured; a wildcard is never accepted and the localhost defaults
// apply only when nothing is configured.
func TestCorsOrigins(t *testing.T) {
	if got := corsOrigins(""); len(got) != 4 || got[0] != "http://localhost:5173" {
		t.Fatalf("defaults: %v", got)
	}
	got := corsOrigins(" https://demo.example.com/ , * ,https://b.example.com,,")
	if want := []string{"https://demo.example.com", "https://b.example.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if got := corsOrigins("*"); len(got) != 0 {
		t.Fatalf("a wildcard must yield no origins, got %v", got)
	}
}
