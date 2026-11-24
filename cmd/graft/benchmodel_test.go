package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBenchModelOverride(t *testing.T) {
	cur := &modelCfg{Argv: []string{"claude", "-p"}, TimeoutSec: 90}
	got, _ := overrideModel(cur, "  claude  -p --model haiku ")
	if !reflect.DeepEqual(got.Argv, []string{"claude", "-p", "--model", "haiku"}) || got.TimeoutSec != 90 {
		t.Fatalf("%+v", got)
	}
	if cur.Argv[0] != "claude" || len(cur.Argv) != 2 {
		t.Fatal("the config's model was modified")
	}
	if g, _ := overrideModel(nil, "m x"); len(g.Argv) != 2 || g.TimeoutSec != 0 {
		t.Fatalf("%+v", g)
	}
}

// The recorded model field is the override's argv (plus the CLI's version).
func TestBenchModelNameRecordsTheOverride(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "fakemodel")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'fakemodel 9.9'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m, _ := overrideModel(&modelCfg{Argv: []string{"other"}}, fake+" --model haiku")
	argv := m.Argv
	name := benchModelName(argv)
	if !strings.HasPrefix(name, fake+" --model haiku") || !strings.HasSuffix(name, "[fakemodel 9.9]") || strings.Contains(name, "other") {
		t.Fatal(name)
	}
}

func TestBenchModelJSONArgv(t *testing.T) {
	m, err := overrideModel(nil, `["claude","-p","--tools","","--model","haiku"]`)
	if err != nil || !reflect.DeepEqual(m.Argv, []string{"claude", "-p", "--tools", "", "--model", "haiku"}) {
		t.Fatalf("%+v %v", m, err)
	}
	for _, bad := range []string{`["a",1]`, `["a"] x`, `[]`, `[""]`, "  "} {
		if _, err := overrideModel(nil, bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
