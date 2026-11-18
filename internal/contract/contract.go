// Package contract is graft's view of a sedum generator package's convergence
// contract: the types sedum convergence info and render speak, and the exec
// implementation that runs them. It knows no language.
package contract

// Contract is the generator package's convergence contract as sedum applies it
// (sedum convergence info / render). Plan reads it so that nothing about a
// language is written in graft: Go and TypeScript differ only in the block a
// generator package declares.
type Contract interface {
	Info() (Info, error)
	Render(Input) (Output, error)
}

// Info is `sedum convergence info`.
type Info struct {
	Package       string   `json:"package"`
	Extensions    []string `json:"extensions"`
	CommentPrefix string   `json:"comment_prefix"`
	SynthSuffix   string   `json:"synth_suffix"`
	Placeholder   string   `json:"placeholder"`
}

// File is one skeleton file as the contract sees it.
type File struct {
	Path    string   `json:"path"`
	Package string   `json:"package,omitempty"`
	Imports []string `json:"imports,omitempty"`
	Stubs   []Stub   `json:"stubs,omitempty"`
}

// Input is the stdin of `sedum convergence render`.
type Input struct {
	Files []File `json:"files"`
}

// RenderedStub is one rendered stub.
type RenderedStub struct {
	Name    string            `json:"name"`
	Action  string            `json:"action"`
	Kwargs  map[string]string `json:"kwargs"`
	Helper  string            `json:"helper"`
	Stub    string            `json:"stub"`
	Declare string            `json:"declare"`
}

// RenderedFile is one rendered file: the invocations that put its imports
// and stubs in place, its synth path, and the seed of that synth file.
type RenderedFile struct {
	Path        string              `json:"path"`
	SynthPath   string              `json:"synth_path"`
	SynthImport string              `json:"synth_import"`
	Seed        string              `json:"seed"`
	Imports     []map[string]string `json:"imports"`
	Stubs       []RenderedStub      `json:"stubs"`
}

// Output is the stdout of `sedum convergence render`.
type Output struct {
	Package     string         `json:"package"`
	SynthSuffix string         `json:"synth_suffix"`
	Placeholder string         `json:"placeholder"`
	Files       []RenderedFile `json:"files"`
}

// Param is a named parameter.
type Param struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Stub is a function or method whose body is left to convergence.
type Stub struct {
	Name         string   `json:"name"`
	ReceiverName string   `json:"receiver_name,omitempty"` // e.g. "c"
	ReceiverType string   `json:"receiver_type,omitempty"` // e.g. "*Client" or "Server"
	Params       []Param  `json:"params,omitempty"`
	Results      []string `json:"results,omitempty"`
	// Doc is the contract the stub's doc comment states (no comment markers).
	// It is where conventions that every body must agree on live.
	Doc string `json:"doc,omitempty"`
}
