// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// Package spiffe contains generator-side constants for the SPIFFE identity
// grammar. Generated sys/spiffe ports receive these values through template
// data, and parser validation uses the same constants directly.
package spiffe

const (
	SegmentGrammar = `[A-Za-z0-9][A-Za-z0-9._-]*`
	SegmentPattern = `^` + SegmentGrammar + `$`

	// A fleet product's name, the segment after /products
	// (products-layer-2026-11-08, #475): a Kubernetes label's alphabet, since
	// its namespace is tac-<deployment>-<name>, and never a tid's shape, the
	// shape of a tenant's tac-<deployment>-<tid>, nor a reserved tag nor
	// ReservedProductName.
	ProductNameGrammar  = `[a-z][a-z0-9-]{0,30}[a-z0-9]`
	ProductNamePattern  = `^` + ProductNameGrammar + `$`
	TidShapePattern     = `^[0-9a-z]{8}$`
	ReservedProductName = "tacplus"

	SystemSegment      = "system"
	TenantsSegment     = "tenants"
	ProductsSegment    = "products"
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
	ProductsSegment,
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
	SegmentGrammar      string
	SegmentPattern      string
	ProductNameGrammar  string
	ProductNamePattern  string
	TidShapePattern     string
	ReservedProductName string
}

func TemplateData() TemplateGrammar {
	return TemplateGrammar{
		SegmentGrammar:      SegmentGrammar,
		SegmentPattern:      SegmentPattern,
		ProductNameGrammar:  ProductNameGrammar,
		ProductNamePattern:  ProductNamePattern,
		TidShapePattern:     TidShapePattern,
		ReservedProductName: ReservedProductName,
	}
}

func ReservedTagSet() map[string]bool {
	out := make(map[string]bool, len(ReservedSegments))
	for _, segment := range ReservedSegments {
		out[segment] = true
	}
	return out
}
