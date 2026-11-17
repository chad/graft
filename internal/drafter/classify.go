// Package drafter turns intent prose into DRAFT records (SPEC.md §8).
// It proposes and never applies: every draft carries a receipt saying whether
// re-extracting it reproduces it, and identity is mapped onto already-accepted
// records' derived_from links rather than minted.
//
// The classifier is a port of Phoenix's canonicalizer rubric (src/canonicalizer.ts,
// weights from src/experiment-config.ts), including its rescue pass for
// declarative statements — the fix for the bench finding that the bare rubric
// filed "`POST /tasks` — creates a task" as CONTEXT and dropped half of a spec's
// obligations.
package drafter

import (
	"regexp"
	"strings"
)

// Type is a canonical statement type.
type Type string

const (
	Requirement Type = "REQUIREMENT"
	Constraint  Type = "CONSTRAINT"
	Invariant   Type = "INVARIANT"
	Definition  Type = "DEFINITION"
	Context     Type = "CONTEXT"
)

// Rubric weights, from Phoenix's experiment-config.ts.
const (
	wConstraintNegation = 4
	wConstraintLimit    = 3
	wConstraintNumeric  = 2
	wInvariant          = 4
	wReqModal           = 2
	wReqKeyword         = 2
	wReqVerb            = 1
	wDefExplicit        = 4
	wDefColon           = 3
	wCtxNoModal         = 1
	wCtxShort           = 1
	wConstraintMust     = 1
	minConfidence       = 0.3
	rescueConfidence    = 0.5
	maxConfidence       = 1.0
	defMaxLength        = 200
)

func re(s string) *regexp.Regexp { return regexp.MustCompile(s) }

var (
	reNegation   = re(`(?i)\b(?:must not|shall not|may not|cannot|can't|disallowed|forbidden|prohibited)\b`)
	reLimit      = re(`(?i)\b(?:limited to|maximum|minimum|at most|at least|no more than|no fewer than|up to|ceiling|floor)\b`)
	reNumPer     = re(`(?i)\b\d+\s*(?:per|/)\s*\w+\b`)
	reNumCmp     = re(`[≤≥<>]\s*\d+`)
	reInvariant  = re(`(?i)\b(?:always|never|at all times|regardless|invariant|guaranteed|must remain|must always|must never)\b`)
	reMustShall  = re(`(?i)\b(?:must|shall)\b`)
	reMustNotEtc = re(`(?i)\b(?:must not|shall not|must always|must never|must remain)\b`)
	reReqKeyword = re(`(?i)\b(?:required|requires?|needs? to|has to|will)\b`)
	reReqVerb    = re(`(?i)\b(?:support|provide|implement|enable|allow|accept|return|create|delete|update|send|receive|handle|manage|track|store|validate|generate)\b`)
	reDefExplain = re(`(?i)\b(?:is defined as|means|refers to|is a|is an)\b`)
	reDefColon   = re(`^[A-Z][a-zA-Z\s]{2,30}:\s+[A-Z]`)
	reEndsEnum   = re(`[:,]\s*$`)
	reAnyModal   = re(`\b(?:must|shall|should|will|required|requires?|needs? to|has to|cannot|forbidden|prohibited)\b`)
	reAnyKeyword = re(`\b(?:support|provide|implement|enable|allow|accept|return|create|delete|update|send|receive|handle|manage|track|store|validate|generate|defined|means|refers)\b`)

	// Rescue signals (Phoenix rescueFromContext).
	reShellRoute   = re("(?i)^`?(?:GET|POST|PUT|PATCH|DELETE)\\s+/(?:health|healthz|ready|readyz|metrics|favicon\\.ico)\\b")
	reRouteDecl    = re("(?i)^`?(?:GET|POST|PUT|PATCH|DELETE)\\s+/\\S*")
	reStatusDecl   = re("^`?[1-5]\\d\\d`?\\s*(?:[—–:-]|\\s-\\s)")
	reEntityShape  = re("(?i)^(?:an?|each|every|the)\\s+[a-z][a-z -]{0,40}?\\s+(?:(?:has|have|records|contains|is\\s+one\\s+that|is\\s+the\\s+number\\s+of|is\\s+an?\\s+json\\s+object)\\b|is\\s+`?\\{)")
	reEnum         = re(`(?i)\b(?:is|are|must be)\s+(?:exactly\s+)?one\s+of\b|\bdefaults?\s+to\b`)
	reFormula      = re(`(?i)\bis\s+(?:the\s+)?(?:sum|total|difference|product|number|count|average|mean)\s+of\b|\bdivided\s+by\b|\brounded\s+(?:down|up)\b|\bleft\s+over\b`)
	reModeRule     = re(`(?i)^in\s+the\s+["“]?[\w ]{1,30}["”]?\s+mode\b`)
	reErrorBody    = re(`(?i)^(?:every|each|all)\s+(?:[1-5]\d\d[,\s]*(?:and|or)?\s*)+(?:responses?|errors?)\b`)
	reStatusMent   = re(`(?i)\b(?:returns?|responds?|answers?|reports?)\s+(?:with\s+)?(?:an?\s+)?(?:status\s+)?[1-5]\d\d\b`)
	reDuration     = re(`(?i)\b\d+\s+(?:seconds?|minutes?|hours?|days?|weeks?|months?|years?)\b`)
	reActorOrThing = re(`(?i)^(?:an?|each|every|the)\s`)
	reConditional  = re(`(?i)^(?:when|whenever|if|once|after|while)\b`)
	reQuoted       = re(`["“][^"”]{2,}["”]`)
	reUIVerb       = re(`(?i)\b(?:shows?|showing|says?|reads?|displays?|labell?ed|titled|named|heading|button|field|link|message|text)\b`)
	// RE2 has no lookahead: Phoenix's `(?:can|may)\s+(?!not\b)` and the
	// generic-plural exclusion list are checked in code after matching.
	reCapability    = re(`(?i)\b(?:users?|clients?|callers?|customers?|admins?|administrators?|members?|anyone|people|the system|the service|it|they)\s+(?:can|may)\s+(\w+)`)
	reGenericPlural = re(`(?i)^([a-z]+s)\s+(?:have|are|get|must|should)\b`)
	pluralExcluded  = map[string]bool{"users": true, "people": true, "clients": true, "callers": true, "customers": true, "admins": true, "members": true, "they": true, "items": true}

	reActionVerb = buildActionVerbRE()
)

func buildActionVerbRE() *regexp.Regexp {
	verbs := []string{"support", "provide", "implement", "enable", "allow", "accept", "return", "create", "delete",
		"update", "send", "receive", "handle", "manage", "track", "store", "validate", "generate",
		"list", "read", "register", "add", "remove", "borrow", "report", "reject", "expose", "answer",
		"mark", "compute", "count", "record", "filter", "reopen", "complete", "edit", "rename", "archive",
		"show", "display", "follow", "write", "ignore", "split", "suggest", "open", "close", "serve"}
	var alts []string
	for _, v := range verbs {
		stem := strings.TrimSuffix(v, "e")
		alts = append(alts, v, v+"s", v+"d", v+"ed", stem+"ing", v+"es")
	}
	return re(`(?i)\b(?:` + strings.Join(alts, "|") + `)\b`)
}

func words(s string) int { return len(strings.Fields(s)) }

// Classify scores a statement's own text (never its heading) and returns its
// type and confidence. fromList marks a statement the author enumerated.
func Classify(text string, fromList bool) (Type, float64) {
	text = strings.NewReplacer("‘", "'", "’", "'", "ʼ", "'", "‛", "'").Replace(text)
	lower := strings.ToLower(text)
	s := map[Type]int{}

	if reNegation.MatchString(text) {
		s[Constraint] += wConstraintNegation
	}
	if reLimit.MatchString(text) {
		s[Constraint] += wConstraintLimit
	}
	if reNumPer.MatchString(text) || reNumCmp.MatchString(text) {
		s[Constraint] += wConstraintNumeric
	}
	if reInvariant.MatchString(text) {
		s[Invariant] += wInvariant
	}
	if reMustShall.MatchString(text) && !reMustNotEtc.MatchString(text) {
		s[Requirement] += wReqModal
	}
	if reReqKeyword.MatchString(text) {
		s[Requirement] += wReqKeyword
	}
	if reReqVerb.MatchString(text) {
		s[Requirement] += wReqVerb
	}
	if reDefExplain.MatchString(text) && len(text) < defMaxLength {
		s[Definition] += wDefExplicit
	}
	if reDefColon.MatchString(text) && !reEndsEnum.MatchString(text) {
		s[Definition] += wDefColon
	}
	modal := reAnyModal.MatchString(lower)
	if !modal && !reAnyKeyword.MatchString(lower) {
		s[Context] += wCtxNoModal
	}
	if words(text) < 8 && !modal {
		s[Context] += wCtxShort
	}
	if reMustShall.MatchString(text) {
		s[Constraint] += wConstraintMust
	}

	// Highest score wins; ties break in Phoenix's declaration order.
	order := []Type{Requirement, Constraint, Invariant, Definition, Context}
	win, winScore, runner := Context, 0, 0
	for _, t := range order {
		if v := s[t]; v > winScore {
			runner, win, winScore = winScore, t, v
		} else if v > runner {
			runner = v
		}
	}
	if winScore == 0 || win == Context {
		if t, ok := rescue(text, fromList); ok {
			return t, rescueConfidence
		}
	}
	if winScore == 0 {
		return Context, minConfidence
	}
	c := float64(winScore-runner) / float64(max(winScore, 1))
	return win, min(maxConfidence, max(minConfidence, c))
}

// rescue promotes declarative obligations the modal rubric has no words for.
// It only ever rescues a statement filed as CONTEXT.
func rescue(text string, fromList bool) (Type, bool) {
	switch {
	case reRouteDecl.MatchString(text):
		if reShellRoute.MatchString(text) {
			return "", false
		}
		return Requirement, true
	case reStatusDecl.MatchString(text), reEntityShape.MatchString(text):
		return Definition, true
	case reEnum.MatchString(text), reFormula.MatchString(text), reModeRule.MatchString(text),
		reErrorBody.MatchString(text), genericPlural(text):
		return Definition, true
	case reStatusMent.MatchString(text):
		return Requirement, true
	}
	if !fromList {
		return "", false
	}
	switch {
	case reDuration.MatchString(text) && reActorOrThing.MatchString(text):
		return Constraint, true
	case capability(text):
		return Requirement, true
	case reConditional.MatchString(text) && words(text) >= 5:
		return Requirement, true
	case reQuoted.MatchString(text) && reUIVerb.MatchString(text):
		return Requirement, true
	case reActionVerb.MatchString(text) && words(text) >= 5:
		return Requirement, true
	}
	return "", false
}

func capability(text string) bool {
	for _, m := range reCapability.FindAllStringSubmatch(text, -1) {
		if !strings.EqualFold(m[1], "not") {
			return true
		}
	}
	return false
}

func genericPlural(text string) bool {
	m := reGenericPlural.FindStringSubmatch(text)
	return m != nil && !pluralExcluded[strings.ToLower(m[1])]
}
