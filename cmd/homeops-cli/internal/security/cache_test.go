package security

import (
	"testing"
	"time"

	homeopserrors "homeops-cli/internal/errors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecretCacheStoreRetrieveAndMetadata(t *testing.T) {
	cache, err := NewSecretCache("super-secret", time.Hour)
	require.NoError(t, err)

	require.NoError(t, cache.Store("token", []byte("value")))
	assert.Equal(t, 1, cache.Size())
	assert.True(t, cache.Exists("token"))

	entry := cache.cache["token"]
	require.NotNil(t, entry)
	assert.NotEqual(t, []byte("value"), entry.EncryptedData)
	assert.NotEmpty(t, entry.Nonce)

	value, err := cache.Retrieve("token")
	require.NoError(t, err)
	assert.Equal(t, []byte("value"), value)

	metadata := cache.GetMetadata()
	require.Len(t, metadata, 1)
	assert.Equal(t, "token", metadata[0].Key)
	assert.Equal(t, int64(1), metadata[0].AccessCount)
	assert.False(t, metadata[0].IsExpired)

	require.NoError(t, cache.Delete("token"))
	assert.Equal(t, 0, cache.Size())

	cache.Clear()
	assert.Empty(t, cache.GetMetadata())
}

func TestSecretCacheExpirationAndCleanup(t *testing.T) {
	cache, err := NewSecretCache("super-secret", time.Hour)
	require.NoError(t, err)

	require.NoError(t, cache.Store("expired", []byte("value"), -1*time.Second))
	assert.False(t, cache.Exists("expired"))
	assert.Equal(t, 1, cache.CleanupExpired())
	assert.Equal(t, 0, cache.Size())

	require.NoError(t, cache.Store("short", []byte("value"), 10*time.Millisecond))
	time.Sleep(25 * time.Millisecond)

	_, err = cache.Retrieve("short")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has expired")
	assert.Equal(t, 0, cache.Size())
}

func TestSecretCacheValidationErrors(t *testing.T) {
	_, err := NewSecretCache("", time.Minute)
	require.Error(t, err)
	assert.True(t, homeopserrors.IsType(err, homeopserrors.ErrTypeSecurity))

	cache, err := NewSecretCache("pw", time.Minute)
	require.NoError(t, err)

	require.Error(t, cache.Store("", []byte("value")))
	require.Error(t, cache.Store("key", nil))
	require.Error(t, cache.Delete(""))
	require.Error(t, cache.Delete("missing"))
	_, err = cache.Retrieve("")
	require.Error(t, err)
	_, err = cache.Retrieve("missing")
	require.Error(t, err)
}

func TestEncryptAndDecryptString(t *testing.T) {
	encrypted, err := EncryptString("payload", "password")
	require.NoError(t, err)
	assert.NotEmpty(t, encrypted)
	assert.NotEqual(t, "payload", encrypted)

	decrypted, err := DecryptString(encrypted, "password")
	require.NoError(t, err)
	assert.Equal(t, "payload", decrypted)

	_, err = EncryptString("payload", "")
	require.Error(t, err)

	_, err = DecryptString(encrypted, "")
	require.Error(t, err)

	_, err = DecryptString("not-base64", "password")
	require.Error(t, err)

	_, err = DecryptString(encrypted, "wrong-password")
	require.Error(t, err)
}
