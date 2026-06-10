package errors

import (
	stderrors "errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConstructors(t *testing.T) {
	cause := stderrors.New("boom")

	tests := []struct {
		name string
		got  *HomeOpsError
		typ  ErrorType
	}{
		{"template", NewTemplateError("TPL", "template failed", cause), ErrTypeTemplate},
		{"validation", NewValidationError("VAL", "validation failed", cause), ErrTypeValidation},
		{"security", NewSecurityError("SEC", "security failed", cause), ErrTypeSecurity},
		{"filesystem", NewFileSystemError("FS", "filesystem failed", cause), ErrTypeFileSystem},
		{"notfound", NewNotFoundError("NF", "missing", cause), ErrTypeNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NotNil(t, tt.got)
			assert.Equal(t, tt.typ, tt.got.Type)
			assert.ErrorIs(t, tt.got.Unwrap(), cause)
			assert.True(t, IsType(tt.got, tt.typ))
		})
	}

	assert.False(t, IsType(stderrors.New("plain"), ErrTypeTemplate))
}

func TestHomeOpsErrorFormatting(t *testing.T) {
	cause := stderrors.New("disk full")
	err := &HomeOpsError{
		Type:    ErrTypeFileSystem,
		Code:    "FS001",
		Message: "write failed",
		Cause:   cause,
	}

	assert.Contains(t, err.Error(), "FS001: write failed")
	assert.Contains(t, err.Error(), "disk full")

	noCause := &HomeOpsError{Code: "GENERIC", Message: "plain error"}
	assert.Equal(t, "GENERIC: plain error", noCause.Error())
}
