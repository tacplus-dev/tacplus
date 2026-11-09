// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// Package accountingpolicy is the one definition of how a call's accounting
// record is classified (tacplus#273): its typed outcome from the gRPC status
// code, its typed reason from where in the generated call path it stopped, and
// its level from the outcome. Go, Java and Rust render their am classification
// from these tables, so the three cannot drift.
package accountingpolicy

// Stages of a generated call path, in order. A wrapper records the stage it
// reached; the reason of a call that stops is a function of that stage and the
// call's outcome.
const (
	// StageAuth: authentication and the method's declared auth check.
	StageAuth = "AUTH"
	// StageRequest: request validation and the declared guards that run on
	// the request before the handler (namespace, owner, tenant, device pins).
	StageRequest = "REQUEST"
	// StageHandler: the handler.
	StageHandler = "HANDLER"
	// StageResponse: the guards, filters and validation run on the response.
	StageResponse = "RESPONSE"
)

// Stages lists the stages in call order.
var Stages = []string{StageAuth, StageRequest, StageHandler, StageResponse}

// Outcome suffixes of accounting.RpcOutcome (RPC_OUTCOME_<suffix>).
const (
	OutcomePermitted   = "PERMITTED"
	OutcomeDeniedAuthn = "DENIED_AUTHN"
	OutcomeDeniedAuthz = "DENIED_AUTHZ"
	OutcomeInvalid     = "INVALID"
	OutcomeFailed      = "FAILED"
)

// Reason suffixes of accounting.RpcReason (RPC_REASON_<suffix>).
const (
	ReasonUndefined       = "UNDEFINED"
	ReasonTokenRejected   = "TOKEN_REJECTED"
	ReasonNotEntitled     = "NOT_ENTITLED"
	ReasonPolicyDenied    = "POLICY_DENIED"
	ReasonRequestInvalid  = "REQUEST_INVALID"
	ReasonHandlerError    = "HANDLER_ERROR"
	ReasonResponseInvalid = "RESPONSE_INVALID"
	ReasonInternal        = "INTERNAL"
)

// CodeOutcome maps one gRPC status code to its outcome.
type CodeOutcome struct {
	Code    int
	Name    string // the canonical gRPC code name, e.g. "PERMISSION_DENIED"
	Outcome string
}

// CodeOutcomes is every code whose outcome is not DefaultOutcome.
var CodeOutcomes = []CodeOutcome{
	{0, "OK", OutcomePermitted},
	{3, "INVALID_ARGUMENT", OutcomeInvalid},
	{5, "NOT_FOUND", OutcomeInvalid},
	{6, "ALREADY_EXISTS", OutcomeInvalid},
	{7, "PERMISSION_DENIED", OutcomeDeniedAuthz},
	{9, "FAILED_PRECONDITION", OutcomeInvalid},
	{11, "OUT_OF_RANGE", OutcomeInvalid},
	{16, "UNAUTHENTICATED", OutcomeDeniedAuthn},
}

// DefaultOutcome is the outcome of every code not in CodeOutcomes.
const DefaultOutcome = OutcomeFailed

// StageReason is the reason of a call that stopped at Stage with Outcome.
type StageReason struct {
	Stage   string
	Outcome string
	Reason  string
}

// StageReasons covers every stage and every outcome other than PERMITTED,
// whose reason is always UNDEFINED.
var StageReasons = []StageReason{
	{StageAuth, OutcomeDeniedAuthn, ReasonTokenRejected},
	{StageAuth, OutcomeDeniedAuthz, ReasonNotEntitled},
	{StageAuth, OutcomeInvalid, ReasonRequestInvalid},
	{StageAuth, OutcomeFailed, ReasonInternal},
	{StageRequest, OutcomeDeniedAuthn, ReasonTokenRejected},
	{StageRequest, OutcomeDeniedAuthz, ReasonPolicyDenied},
	{StageRequest, OutcomeInvalid, ReasonRequestInvalid},
	{StageRequest, OutcomeFailed, ReasonInternal},
	{StageHandler, OutcomeDeniedAuthn, ReasonHandlerError},
	{StageHandler, OutcomeDeniedAuthz, ReasonHandlerError},
	{StageHandler, OutcomeInvalid, ReasonHandlerError},
	{StageHandler, OutcomeFailed, ReasonHandlerError},
	{StageResponse, OutcomeDeniedAuthn, ReasonTokenRejected},
	{StageResponse, OutcomeDeniedAuthz, ReasonPolicyDenied},
	{StageResponse, OutcomeInvalid, ReasonPolicyDenied},
	{StageResponse, OutcomeFailed, ReasonResponseInvalid},
}

// OutcomeLevel is the accounting.EventLevel suffix (EVENT_LEVEL_<suffix>) of
// an RpcCompleted with the outcome.
var OutcomeLevel = map[string]string{
	OutcomePermitted:   "INFO",
	OutcomeDeniedAuthn: "WARN",
	OutcomeDeniedAuthz: "WARN",
	OutcomeInvalid:     "WARN",
	OutcomeFailed:      "ERROR",
}

// OutcomeLevels is OutcomeLevel in a fixed order, for templates.
type OutcomeLevelRow struct{ Outcome, Level string }

// OutcomeLevelRows returns OutcomeLevel in outcome order.
func OutcomeLevelRows() []OutcomeLevelRow {
	var rows []OutcomeLevelRow
	for _, o := range []string{OutcomePermitted, OutcomeDeniedAuthn, OutcomeDeniedAuthz, OutcomeInvalid, OutcomeFailed} {
		rows = append(rows, OutcomeLevelRow{o, OutcomeLevel[o]})
	}
	return rows
}

// Classify is the reference classification the templates render: the
// outcome of code, and the reason of a call that stopped at stage.
func Classify(stage string, code int) (outcome, reason string) {
	outcome = DefaultOutcome
	for _, c := range CodeOutcomes {
		if c.Code == code {
			outcome = c.Outcome
		}
	}
	if outcome == OutcomePermitted {
		return outcome, ReasonUndefined
	}
	for _, r := range StageReasons {
		if r.Stage == stage && r.Outcome == outcome {
			return outcome, r.Reason
		}
	}
	return outcome, ReasonInternal
}

// InternalHeader is the gRPC metadata key a call made to store accounting
// carries (the accounting_internal marker of tacplus#273). A wrapper that sees
// it on a call whose verified caller is a service (token sub_type "service")
// emits no accounting for the call, a backstop to the by-type exemption of the
// accounting topic's own RPCs. A user's call is recorded with or without it.
const InternalHeader = "x-tacplus-accounting-internal"

// Context is the template data every language's am renders.
type Context struct {
	Stages         []string
	CodeOutcomes   []CodeOutcome
	DefaultOutcome string
	StageReasons   []StageReason
	OutcomeLevels  []OutcomeLevelRow
	InternalHeader string
}

// TemplateContext returns the tables for a template.
func TemplateContext() Context {
	return Context{
		Stages:         Stages,
		CodeOutcomes:   CodeOutcomes,
		DefaultOutcome: DefaultOutcome,
		StageReasons:   StageReasons,
		OutcomeLevels:  OutcomeLevelRows(),
		InternalHeader: InternalHeader,
	}
}
