package skeldraft

import (
	"fmt"
	"strings"

	"github.com/chad/graft/internal/drafter"
)

const schema = `{
  "files": [
    {
      "path": "channels.go",
      "section": "<the section's exact title from the list below>",
      "imports": ["sync"],
      "decls": [ {"kind": "type", "name": "channel", "code": "type channel struct {\n\tname string\n}"} ],
      "stubs": [
        {"name": "handleJoin", "receiver_name": "s", "receiver_type": "*Server",
         "params": [{"name": "c", "type": "*Client"}], "results": ["error"],
         "doc": "the contract every body must satisfy, in prose"}
      ]
    }
  ],
  "owners": { "S3": ["channels.go#handleJoin"], "S4": ["channels.go#channel"] }
}`

func prompt(cfg Config, drafts []drafter.Draft, stmts []Statement, feedback string) string {
	var b strings.Builder
	name := cfg.Profile.Name
	fmt.Fprintf(&b, "You are designing the skeleton of a %s program: the shape of each file, before anyone writes a function body. A deterministic layer renders your skeleton; separate workers later write each body, one file at a time, seeing only that file's section, the skeleton and the conventions. So the skeleton must put every cross-file contract in writing.\n\n", name)
	fmt.Fprintf(&b, "## Conventions\n\n%s\n\n", strings.TrimSpace(cfg.Conventions))
	fmt.Fprintf(&b, "## The spec, by section. Statements are numbered; every one must be owned\n\n")
	n := 0
	for _, d := range drafts {
		fmt.Fprintf(&b, "### %s\n\n", d.Section)
		for _, s := range d.Statements {
			n++
			fmt.Fprintf(&b, "- S%d [%s] %s\n", n, s.Type, s.Text)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "## What to produce\n\nPropose, for each section above, the file or files it owns. A file has:\n- `decls`: fixed declarations, written verbatim as the file will contain them. ONLY types, consts and vars. No function or method (those are stubs), no function bodies, no function literals, no calls. A const may use only literals, names, operators and parentheses. A var may have no initialiser, or an empty composite literal such as `map[string]int{}`. A decl with logic is rejected.\n- `stubs`: functions and methods whose BODY is left open, with their signature and a `doc` contract stating what every body must do. Name the types and helpers other files depend on here and in decls, so each file compiles against the skeleton alone. The program's entry point (for example `main`) is a stub too: declare it with its signature and say in its doc what it must do, because the tree has to build with every body still a placeholder.\n- `imports`: the imports the file's decls and stub signatures need.\n\nThe language is %s (%s source files, code in `%s`). Every `section` must be copied exactly from a heading above. Give each section at least one file. Answer with ONE json block of this shape (the example is illustrative; use the spec's own names):\n\n```json\n%s\n```\n\n", name, cfg.Profile.Ext, cfg.Profile.Fence, schema)
	b.WriteString("`owners` is your claim of which stubs and decls own each numbered statement (`path#name`). Be honest: a statement no stub or decl owns is reported to the human as UNOWNED, which is better than a false claim. Statements that only the code's runtime behaviour can satisfy still need an owner, normally the stub that carries them out.\n")
	if feedback != "" {
		fmt.Fprintf(&b, "\n## Your previous answer was rejected\n\n%s\n\nFix exactly this and answer again with the complete json block.\n", feedback)
	}
	return b.String()
}
