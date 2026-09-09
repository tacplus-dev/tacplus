// Copyright (c) 2026 Second Star Cavalier LLC

// Package spiffe contains generator-side constants for the SPIFFE identity
// grammar. Generated sys/spiffe ports receive these values through template
// data, and parser validation uses the same constants directly.
package spiffe

const (
	SegmentGrammar = `[A-Za-z0-9][A-Za-z0-9._-]*`
	SegmentPattern = `^` + SegmentGrammar + `$`

	SystemSegment      = "system"
	TenantsSegment     = "tenants"
	PeopleSegment      = "people"
	ServicesSegment    = "services"
	WorkloadsSegment   = "workloads"
	UsersSegment       = "users"
	WorkspacesSegment  = "workspaces"
	ClientsSegment     = "clients"
	CredentialsSegment = "credentials"
	DataSegment        = "data"
)

var ReservedSegments = []string{
	SystemSegment,
	TenantsSegment,
	PeopleSegment,
	ServicesSegment,
	WorkloadsSegment,
	UsersSegment,
	WorkspacesSegment,
	ClientsSegment,
	CredentialsSegment,
	DataSegment,
}

type TemplateGrammar struct {
	SegmentGrammar string
	SegmentPattern string
}

func TemplateData() TemplateGrammar {
	return TemplateGrammar{
		SegmentGrammar: SegmentGrammar,
		SegmentPattern: SegmentPattern,
	}
}

func ReservedTagSet() map[string]bool {
	out := make(map[string]bool, len(ReservedSegments))
	for _, segment := range ReservedSegments {
		out[segment] = true
	}
	return out
}
