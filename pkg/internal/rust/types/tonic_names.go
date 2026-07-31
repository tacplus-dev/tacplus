// Copyright (c) 2026 Second Star Cavalier LLC

package types

// tonicServiceTypeName returns the Rust type name that tonic/prost generates
// for a proto service. This mirrors heck's UpperCamelCase behavior through
// toProstTypeName, so digit-acronym names like ContractS2S become ContractS2s.
func tonicServiceTypeName(serviceName string) string {
	return toProstTypeName(serviceName)
}

// tonicServiceModuleName returns the Rust module basename that tonic/prost
// generates for a proto service. For example, ContractS2S becomes contract_s2s,
// yielding contract_s2s_client and contract_s2s_server.
func tonicServiceModuleName(serviceName string) string {
	return prostSnakeName(serviceName)
}
