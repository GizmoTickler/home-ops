package errors

import (
	stderrors "errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConstructorsAndWrapHelpers(t *testing.T) {
	cause := stderrors.New("boom")

	tests := []struct {
		name string
		got  *HomeOpsError
		typ  ErrorType
	}{
		{"template", NewTemplateError("TPL", "template failed", cause), ErrTypeTemplate},
		{"kubernetes", NewKubernetesError("K8S", "k8s failed", cause), ErrTypeKubernetes},
		{"talos", NewTalosError("TALOS", "talos failed", cause), ErrTypeTalos},
		{"validation", NewValidationError("VAL", "validation failed", cause), ErrTypeValidation},
		{"network", NewNetworkError("NET", "network failed", cause), ErrTypeNetwork},
		{"config", NewConfigError("CFG", "config failed", cause), ErrTypeConfig},
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
			typ, ok := GetType(tt.got)
			require.True(t, ok)
			assert.Equal(t, tt.typ, typ)
		})
	}

	assert.Nil(t, Wrap(nil, ErrTypeTemplate, "CODE", "message"))
	assert.Equal(t, ErrTypeTemplate, WrapTemplate(cause, "wrapped").Type)
	assert.Equal(t, ErrTypeKubernetes, WrapKubernetes(cause, "wrapped").Type)
	assert.Equal(t, ErrTypeTalos, WrapTalos(cause, "wrapped").Type)
	assert.Equal(t, ErrTypeValidation, WrapValidation(cause, "wrapped").Type)
	assert.Equal(t, ErrTypeNetwork, WrapNetwork(cause, "wrapped").Type)
	assert.Equal(t, ErrTypeConfig, WrapConfig(cause, "wrapped").Type)
	assert.Equal(t, ErrTypeFileSystem, WrapFileSystem(cause, "wrapped").Type)
	assert.False(t, IsType(stderrors.New("plain"), ErrTypeTemplate))
	_, ok := GetType(stderrors.New("plain"))
	assert.False(t, ok)
}

func TestHomeOpsErrorFormattingAndContextHelpers(t *testing.T) {
	cause := stderrors.New("disk full")
	err := (&HomeOpsError{
		Type:    ErrTypeFileSystem,
		Code:    "FS001",
		Message: "write failed",
		Cause:   cause,
	}).WithDetail("path", "/tmp/config.yaml").
		WithContext("save", "bootstrap").
		WithRequestID("req-123").
		WithStackTrace()

	assert.Contains(t, err.Error(), "FS001: write failed")
	assert.Contains(t, err.Error(), "disk full")
	assert.Equal(t, "/tmp/config.yaml", err.Details["path"])
	require.NotNil(t, err.Context)
	assert.Equal(t, "save", err.Context.Operation)
	assert.Equal(t, "bootstrap", err.Context.Component)
	assert.Equal(t, "req-123", err.Context.RequestID)
	assert.NotZero(t, err.Context.Timestamp)
	assert.NotEmpty(t, err.Context.StackTrace)

	noCause := &HomeOpsError{Code: "GENERIC", Message: "plain error"}
	assert.Equal(t, "GENERIC: plain error", noCause.Error())
}

func TestUserFriendlyMessagesForAllTypes(t *testing.T) {
	tests := []struct {
		name            string
		errType         ErrorType
		expectDocsLinks bool
	}{
		{"template", ErrTypeTemplate, true},
		{"kubernetes", ErrTypeKubernetes, true},
		{"talos", ErrTypeTalos, true},
		{"validation", ErrTypeValidation, false},
		{"network", ErrTypeNetwork, false},
		{"config", ErrTypeConfig, false},
		{"security", ErrTypeSecurity, false},
		{"filesystem", ErrTypeFileSystem, false},
		{"notfound", ErrTypeNotFound, false},
		{"default", ErrorType("unknown"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &HomeOpsError{
				Type:    tt.errType,
				Code:    "CODE",
				Message: "message",
				Cause:   stderrors.New("boom"),
			}
			msg := err.GetUserFriendlyMessage()
			require.NotNil(t, msg)
			assert.NotEmpty(t, msg.UserMessage)
			assert.NotEmpty(t, msg.TechnicalDetails)
			assert.NotEmpty(t, msg.SuggestedActions)
			if tt.expectDocsLinks {
				assert.NotEmpty(t, msg.DocumentationLinks)
			}
		})
	}
}
