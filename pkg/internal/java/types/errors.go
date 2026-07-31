// Copyright (c) 2026 Second Star Cavalier LLC

package java

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/header"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/consts"
	"gitlab.com/secondstarcavalier/schematic/tacplus/templates"
)

// errorsTemplateData holds all pre-computed data for the errors template.
type errorsTemplateData struct {
	Header      string
	PackagePath string
	Body        string
}

// generateErrorUtils generates ErrorUtils.java with gRPC error helper functions
func generateErrorUtils(pf model.ProtoFile, baseDir, subdir string) error {
	// Only generate for errors.proto (errors package)
	if filepath.Base(pf.RelPath) != "errors.proto" {
		return nil
	}

	// Find the Error message
	var errorMsg *model.Message
	for i := range pf.Messages {
		if pf.Messages[i].Name == "Error" {
			errorMsg = &pf.Messages[i]
			break
		}
	}

	if errorMsg == nil {
		return nil // No Error message found
	}

	outputPath := filepath.Join(baseDir, subdir, "ErrorUtils.java")
	outputDir := filepath.Dir(outputPath)

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}

	// Determine package name from subdir
	packageName := strings.ReplaceAll(subdir, string(filepath.Separator), ".")

	// Pre-render the body
	var body strings.Builder

	// gRPC status code constants
	body.WriteString("// gRPC status codes (from io.grpc.Status.Code)\n")
	body.WriteString("public static final int INVALID_ARGUMENT = 3;\n")
	body.WriteString("public static final int NOT_FOUND = 5;\n")
	body.WriteString("public static final int DEADLINE_EXCEEDED = 4;\n")
	body.WriteString("public static final int UNAUTHENTICATED = 16;\n")
	body.WriteString("public static final int INTERNAL = 13;\n")
	body.WriteString("public static final int CANCELLED = 1;\n")
	body.WriteString("public static final int UNKNOWN = 2;\n")
	body.WriteString("public static final int PERMISSION_DENIED = 7;\n")
	body.WriteString("public static final int ALREADY_EXISTS = 6;\n")
	body.WriteString("public static final int RESOURCE_EXHAUSTED = 8;\n")
	body.WriteString("public static final int FAILED_PRECONDITION = 9;\n")
	body.WriteString("public static final int ABORTED = 10;\n")
	body.WriteString("public static final int OUT_OF_RANGE = 11;\n")
	body.WriteString("public static final int UNIMPLEMENTED = 12;\n")
	body.WriteString("public static final int UNAVAILABLE = 14;\n")

	// Helper method to format message
	body.WriteString("/**\n")
	body.WriteString("* Formats error message, optionally including cause.\n")
	body.WriteString("* @param cause Optional causing exception\n")
	body.WriteString("* @param format Message format string\n")
	body.WriteString("* @param args Format arguments\n")
	body.WriteString("* @return Formatted message\n")
	body.WriteString("*/\n")
	body.WriteString("private static String getMessage(Throwable cause, String format, Object... args) {\n")
	body.WriteString("if (cause == null && (format == null || format.isEmpty())) {\n")
	body.WriteString("return \"\";\n")
	body.WriteString("}\n")
	body.WriteString("String formatted = (format == null || format.isEmpty()) ? \"\" : String.format(format, args);\n")
	body.WriteString("if (cause != null && !formatted.isEmpty()) {\n")
	body.WriteString("return formatted + \": \" + cause.getMessage();\n")
	body.WriteString("}\n")
	body.WriteString("if (cause != null) {\n")
	body.WriteString("return cause.getMessage();\n")
	body.WriteString("}\n")
	body.WriteString("return formatted;\n")
	body.WriteString("}\n")

	// Error creation functions
	errorFuncs := []struct {
		name        string
		code        string
		description string
		hasCause    bool
	}{
		{"newErrMissingInvalidData", "INVALID_ARGUMENT", "Creates an error for missing or invalid data. Use when client specified an invalid argument (invalid field value, missing required field).", true},
		{"newErrNotFound", "NOT_FOUND", "Creates an error for resource not found. Use when a requested resource does not exist.", false},
		{"newErrExpired", "DEADLINE_EXCEEDED", "Creates an error for expired resources or deadline exceeded. Use when an operation deadline has expired or a resource has expired.", false},
		{"newErrUnauthorized", "UNAUTHENTICATED", "Creates an error for authentication failures. Use when the request does not have valid authentication credentials. For authorization failures (caller identified but lacks permission), use newErrPermissionDenied.", true},
		{"newErrGeneral", "INTERNAL", "Creates a general internal server error. Use when an internal invariant has been violated (server bugs, unexpected failures). This should be used sparingly - prefer more specific error codes when possible.", true},
		{"newErrCancelled", "CANCELLED", "Creates an error for cancelled operations. Use when an operation is cancelled (typically by the caller).", false},
		{"newErrUnknown", "UNKNOWN", "Creates an unknown error. Use when you don't know which error code to use.", true},
		{"newErrPermissionDenied", "PERMISSION_DENIED", "Creates an error for authorization failures. Use when the caller does not have permission to execute the specified operation. Different from Unauthenticated (which means caller failed to authenticate).", true},
		{"newErrAlreadyExists", "ALREADY_EXISTS", "Creates an error for resources that already exist. Use when attempting to create a resource that already exists.", false},
		{"newErrResourceExhausted", "RESOURCE_EXHAUSTED", "Creates an error for resource exhaustion. Use when a resource has been exhausted (e.g., per-user quota, out of disk space).", false},
		{"newErrFailedPrecondition", "FAILED_PRECONDITION", "Creates an error for failed preconditions. Use when the operation was rejected because the system is not in a required state.", false},
		{"newErrAborted", "ABORTED", "Creates an error for aborted operations. Use when the operation was aborted (e.g., due to a concurrency issue like transaction abort).", false},
		{"newErrOutOfRange", "OUT_OF_RANGE", "Creates an error for out-of-range operations. Use when an operation is attempted past the valid range.", false},
		{"newErrUnimplemented", "UNIMPLEMENTED", "Creates an error for unimplemented operations. Use when the operation is not implemented or not supported/enabled.", false},
		{"newErrUnavailable", "UNAVAILABLE", "Creates an error for unavailable services. Use when the service is currently unavailable (transient condition).", true},
	}

	for _, ef := range errorFuncs {
		body.WriteString("/**\n")
		fmt.Fprintf(&body, "* %s\n", ef.description)
		fmt.Fprintf(&body, "* Maps to gRPC %s status code.\n", ef.code)
		if ef.hasCause {
			body.WriteString("* @param cause Optional causing exception\n")
		}
		body.WriteString("* @param format Message format string\n")
		body.WriteString("* @param args Format arguments\n")
		body.WriteString("* @return ProtoException with appropriate error code\n")
		body.WriteString("*/\n")
		if ef.hasCause {
			fmt.Fprintf(&body, "public static ProtoException %s(Throwable cause, String format, Object... args) {\n", ef.name)
			fmt.Fprintf(&body, "return new ProtoException(%s, getMessage(cause, format, args), cause);\n", ef.code)
		} else {
			fmt.Fprintf(&body, "public static ProtoException %s(String format, Object... args) {\n", ef.name)
			fmt.Fprintf(&body, "return new ProtoException(%s, String.format(format, args));\n", ef.code)
		}
		body.WriteString("}\n")
	}

	// IsErr methods
	body.WriteString("/**\n")
	body.WriteString("* Checks if the exception is a ProtoException with the given error code.\n")
	body.WriteString("* @param ex The exception to check\n")
	body.WriteString("* @param code The error code to match\n")
	body.WriteString("* @return true if the exception is a ProtoException with the given code\n")
	body.WriteString("*/\n")
	body.WriteString("public static boolean isErrCode(Throwable ex, int code) {\n")
	body.WriteString("if (ex instanceof ProtoException) {\n")
	body.WriteString("return ((ProtoException) ex).getCode() == code;\n")
	body.WriteString("}\n")
	body.WriteString("return false;\n")
	body.WriteString("}\n")

	// Specific IsErr methods
	for _, ef := range errorFuncs {
		functionName := strings.TrimPrefix(ef.name, "newErr")
		fmt.Fprintf(&body, "/**\n")
		fmt.Fprintf(&body, "* Checks if the exception is a %s error.\n", ef.code)
		fmt.Fprintf(&body, "* @param ex The exception to check\n")
		fmt.Fprintf(&body, "* @return true if the exception is a %s error\n", ef.code)
		fmt.Fprintf(&body, "*/\n")
		fmt.Fprintf(&body, "public static boolean isErr%s(Throwable ex) {\n", functionName)
		fmt.Fprintf(&body, "return isErrCode(ex, %s);\n", ef.code)
		body.WriteString("}\n")
	}

	// GetErrorCode method
	body.WriteString("/**\n")
	body.WriteString("* Gets the error code from a ProtoException, or 0 if not a ProtoException.\n")
	body.WriteString("* @param ex The exception to extract code from\n")
	body.WriteString("* @return The error code, or 0 if not a ProtoException\n")
	body.WriteString("*/\n")
	body.WriteString("public static int getErrorCode(Throwable ex) {\n")
	body.WriteString("if (ex instanceof ProtoException) {\n")
	body.WriteString("return ((ProtoException) ex).getCode();\n")
	body.WriteString("}\n")
	body.WriteString("return 0; // OK status\n")
	body.WriteString("}\n")

	data := errorsTemplateData{
		Header:      header.GeneratedHeader(consts.LangJava, pf.RelPath),
		PackagePath: packageName,
		Body:        body.String(),
	}

	// Load and execute template
	typeFS, err := fs.Sub(templates.FS, "java/types")
	if err != nil {
		return fmt.Errorf("failed to create sub filesystem: %w", err)
	}
	content, err := fs.ReadFile(typeFS, "errors.java.tmpl")
	if err != nil {
		return fmt.Errorf("failed to read errors template: %w", err)
	}
	tmpl, err := template.New("errors").Parse(string(content))
	if err != nil {
		return fmt.Errorf("failed to parse errors template: %w", err)
	}

	var result strings.Builder
	if err := tmpl.Execute(&result, data); err != nil {
		return fmt.Errorf("failed to execute errors template: %w", err)
	}

	return os.WriteFile(outputPath, []byte(result.String()), 0644)
}
