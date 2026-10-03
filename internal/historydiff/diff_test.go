package historydiff

import (
	"reflect"
	"testing"
)

func TestChanges(t *testing.T) {
	tests := []struct {
		name                          string
		before, after, added, removed []string
	}{
		{"empty", nil, nil, nil, nil},
		{"new top shifts old items", []string{"b", "a"}, []string{"c", "b", "a"}, []string{"c"}, nil},
		{"delete old item", []string{"c", "b", "a"}, []string{"c", "a"}, nil, []string{"b"}},
		{"clear history", []string{"b", "a"}, nil, nil, []string{"b", "a"}},
		{"unchanged", []string{"b", "a"}, []string{"b", "a"}, nil, nil},
		{"reorder only", []string{"b", "a"}, []string{"a", "b"}, nil, nil},
		{"new item and eviction same length", []string{"b", "a"}, []string{"c", "b"}, []string{"c"}, []string{"a"}},
		{"multiple additions", []string{"a"}, []string{"c", "b", "a"}, []string{"c", "b"}, nil},
		{"clear preserves pinned item", []string{"c", "b", "a"}, []string{"b"}, nil, []string{"c", "a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			added, removed := Changes(tt.before, tt.after)
			if !reflect.DeepEqual(added, tt.added) || !reflect.DeepEqual(removed, tt.removed) {
				t.Fatalf("got added=%v removed=%v; want added=%v removed=%v", added, removed, tt.added, tt.removed)
			}
		})
	}
}
