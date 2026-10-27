package platform

import "fmt"

// InputDiagnosticStage identifies a refusal site, never target data.
type InputDiagnosticStage int

// Values mirror rd_input_stage; unknown native values remain safe to format.
const (
	InputStageUnknown InputDiagnosticStage = iota
	InputStageBindSelection
	InputStageCGUnique
	InputStageAXWindows
	InputStageAXTitle
	InputStageFocusedWindow
	InputStageIdentity
	InputStageLocalFirst
	InputStageLocalSecond
	InputStagePrepare
	InputStagePrePost
	InputStageTarget
)

// InputDiagnosticCode describes only the failed predicate.
type InputDiagnosticCode int

// Values mirror rd_input_code.
const (
	InputCodeUnknown InputDiagnosticCode = iota
	InputCodePermission
	InputCodeBudget
	InputCodeAllocation
	InputCodeCGListLimit
	InputCodeMissing
	InputCodeTitle
	InputCodeNotUnique
	InputCodeVisibility
	InputCodeProcessStartChanged
	InputCodeAXTimeoutSetup
	InputCodeQuery
	InputCodeNull
	InputCodeType
	InputCodeWindowLimit
	InputCodeFocusIdentity
	InputCodeFrontmost
	InputCodeModifiers
	InputCodeUTF8
	InputCodeTargetInvalid
)

func (s InputDiagnosticStage) String() string {
	names := [...]string{"unknown", "bind_cg_selection", "cg_unique", "ax_windows", "ax_title", "focused_window", "identity", "local_first", "local_second", "prepare", "pre_post", "target"}
	if s < 0 || int(s) >= len(names) {
		return names[0]
	}
	return names[s]
}

func (c InputDiagnosticCode) String() string {
	names := [...]string{"unknown", "permission", "budget", "allocation", "cg_list_limit", "missing", "title", "not_unique", "visibility", "process_start_changed", "ax_timeout_setup", "query", "null", "type", "window_limit", "focus_identity", "frontmost", "modifiers", "utf8", "target_invalid"}
	if c < 0 || int(c) >= len(names) {
		return names[0]
	}
	return names[c]
}

// InputDiagnosticError is a copy of caller-owned native diagnostics. AXError is
// numeric, including successful AX results rejected for a late return.
type InputDiagnosticError struct {
	Stage      InputDiagnosticStage
	Code       InputDiagnosticCode
	HasAXError bool
	AXError    int
}

func (e *InputDiagnosticError) Error() string {
	message := fmt.Sprintf("%s: stage=%s code=%s", ErrInputUnavailable, e.Stage, e.Code)
	if e.HasAXError {
		message += fmt.Sprintf(" ax=%d", e.AXError)
	}
	return message
}

func (*InputDiagnosticError) Unwrap() error { return ErrInputUnavailable }
