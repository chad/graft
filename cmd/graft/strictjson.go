package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// strictCheck walks a JSON document and refuses what encoding/json lets
// through silently: duplicate keys at any level, null anywhere, trailing data
// after the top-level value, and keys outside an exact (case-sensitive)
// allowed set. allowed maps a path ("" for the top level, "gates[]" for each
// gate object) to its permitted keys; a path not in allowed (e.g. the policy
// object, whose keys are tier names) accepts any key.
func strictCheck(b []byte, allowed map[string][]string) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := walk(dec, "", allowed); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing data after the config object")
	}
	return nil
}

func walk(dec *json.Decoder, path string, allowed map[string][]string) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch t := tok.(type) {
	case nil:
		return fmt.Errorf("%s: null is not allowed", orTop(path))
	case json.Delim:
		switch t {
		case '{':
			seen := map[string]bool{}
			keys, restricted := allowed[path]
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return err
				}
				k := kt.(string)
				if seen[k] {
					return fmt.Errorf("%s: duplicate key %q", orTop(path), k)
				}
				seen[k] = true
				if restricted && !contains(keys, k) {
					return fmt.Errorf("%s: unknown key %q (keys are case-sensitive: %v)", orTop(path), k, keys)
				}
				child := path + "." + k
				if path == "" {
					child = k
				}
				if err := walk(dec, child, allowed); err != nil {
					return err
				}
			}
			_, err := dec.Token() // '}'
			return err
		case '[':
			for dec.More() {
				if err := walk(dec, path+"[]", allowed); err != nil {
					return err
				}
			}
			_, err := dec.Token() // ']'
			return err
		}
	}
	return nil
}

func orTop(p string) string {
	if p == "" {
		return "config"
	}
	return p
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
