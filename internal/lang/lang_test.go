package lang

import (
	"errors"
	"reflect"
	"testing"
)

func TestNilIsGo(t *testing.T) {
	p, err := Resolve(nil)
	if err != nil || !reflect.DeepEqual(p, Go) {
		t.Fatalf("%v %+v", err, p)
	}
}

func TestPartialProfileIsRefused(t *testing.T) {
	if _, err := Resolve(&Profile{Ext: ".py"}); !errors.Is(err, ErrProfile) {
		t.Fatal("a profile missing required fields must be refused, not mixed with Go")
	}
	if _, err := Resolve(&Profile{Name: "py", Ext: "py", Fence: "python", Comment: "#", Run: []string{"python3"}}); !errors.Is(err, ErrProfile) {
		t.Fatal("ext without a dot")
	}
	if _, err := Resolve(&Profile{Name: "py", Ext: ".py", Fence: "python", Comment: "#", Run: []string{"python3"},
		Bootstrap: map[string]string{"../x": ""}}); !errors.Is(err, ErrProfile) {
		t.Fatal("bootstrap escaping the tree")
	}
}
