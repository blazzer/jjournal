package main

import (
	"context"
	"testing"
)

func TestShutdownOrder(t *testing.T) {
	var got []string
	rec := func(name string) func(context.Context) error {
		return func(context.Context) error {
			got = append(got, name)
			return nil
		}
	}
	err := shutdown(context.Background(), []step{
		{"http", rec("http")},
		{"scheduler", rec("scheduler")},
		{"backup", rec("backup")},
		{"store", rec("store")},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"http", "scheduler", "backup", "store"}
	if len(got) != len(want) {
		t.Fatal(got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%v", got)
		}
	}
}
