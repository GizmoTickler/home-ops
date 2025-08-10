package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"homeops-cli/internal/errors"
)

// SecretCache provides secure caching for sensitive data
type SecretCache struct {
	cache      map[string]*CacheEntry
	mu         sync.RWMutex
	encryptKey []byte
	defaultTTL time.Duration
}

// CacheEntry represents a cached secret with metadata
type CacheEntry struct {
	EncryptedData []byte
	Nonce         []byte
	ExpiresAt     time.Time
	CreatedAt     time.Time
	AccessCount   int64
	LastAccessed  time.Time
}

// SecretMetadata provides information about cached secrets without exposing data
type SecretMetadata struct {
	Key          string    `json:"key"`
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	AccessCount  int64     `json:"access_count"`
	LastAccessed time.Time `json:"last_accessed"`
	IsExpired    bool      `json:"is_expired"`
}

// NewSecretCache creates a new secure secret cache
func NewSecretCache(password string, defaultTTL time.Duration) (*SecretCache, error) {
	if password == "" {
		return nil, errors.NewSecurityError("CACHE_INIT_ERROR", 
			"password cannot be empty for secret cache", nil)
	}

	// Derive encryption key from password
	hash := sha256.Sum256([]byte(password))
	encryptKey := hash[:]

	return &SecretCache{
		cache:      make(map[string]*CacheEntry),
		encryptKey: encryptKey,
		defaultTTL: defaultTTL,
	}, nil
}

// Store encrypts and stores a secret in the cache
func (sc *SecretCache) Store(key string, data []byte, ttl ...time.Duration) error {
	if key == "" {
		return errors.NewValidationError("CACHE_STORE_ERROR", 
			"cache key cannot be empty", nil)
	}

	if len(data) == 0 {
		return errors.NewValidationError("CACHE_STORE_ERROR", 
			"data cannot be empty", nil)
	}

	// Determine TTL
	cacheTTL := sc.defaultTTL
	if len(ttl) > 0 {
		cacheTTL = ttl[0]
	}

	// Encrypt the data
	encryptedData, nonce, err := sc.encrypt(data)
	if err != nil {
		return errors.NewSecurityError("CACHE_ENCRYPTION_ERROR", 
			"failed to encrypt secret data", err)
	}

	sc.mu.Lock()
	defer sc.mu.Unlock()

	now := time.Now()
	sc.cache[key] = &CacheEntry{
		EncryptedData: encryptedData,
		Nonce:         nonce,
		ExpiresAt:     now.Add(cacheTTL),
		CreatedAt:     now,
		AccessCount:   0,
		LastAccessed:  now,
	}

	return nil
}

// Retrieve decrypts and returns a secret from the cache
func (sc *SecretCache) Retrieve(key string) ([]byte, error) {
	if key == "" {
		return nil, errors.NewValidationError("CACHE_RETRIEVE_ERROR", 
			"cache key cannot be empty", nil)
	}

	sc.mu.Lock()
	defer sc.mu.Unlock()

	entry, exists := sc.cache[key]
	if !exists {
		return nil, errors.NewNotFoundError("CACHE_KEY_NOT_FOUND", 
			fmt.Sprintf("secret with key '%s' not found in cache", key), nil)
	}

	// Check if expired
	if time.Now().After(entry.ExpiresAt) {
		delete(sc.cache, key)
		return nil, errors.NewValidationError("CACHE_ENTRY_EXPIRED", 
			fmt.Sprintf("secret with key '%s' has expired", key), nil)
	}

	// Update access metadata
	entry.AccessCount++
	entry.LastAccessed = time.Now()

	// Decrypt the data
	data, err := sc.decrypt(entry.EncryptedData, entry.Nonce)
	if err != nil {
		return nil, errors.NewSecurityError("CACHE_DECRYPTION_ERROR", 
			"failed to decrypt secret data", err)
	}

	return data, nil
}

// Delete removes a secret from the cache
func (sc *SecretCache) Delete(key string) error {
	if key == "" {
		return errors.NewValidationError("CACHE_DELETE_ERROR", 
			"cache key cannot be empty", nil)
	}

	sc.mu.Lock()
	defer sc.mu.Unlock()

	if _, exists := sc.cache[key]; !exists {
		return errors.NewNotFoundError("CACHE_KEY_NOT_FOUND", 
			fmt.Sprintf("secret with key '%s' not found in cache", key), nil)
	}

	delete(sc.cache, key)
	return nil
}

// Clear removes all secrets from the cache
func (sc *SecretCache) Clear() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.cache = make(map[string]*CacheEntry)
}

// CleanupExpired removes all expired entries from the cache
func (sc *SecretCache) CleanupExpired() int {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	now := time.Now()
	expiredCount := 0

	for key, entry := range sc.cache {
		if now.After(entry.ExpiresAt) {
			delete(sc.cache, key)
			expiredCount++
		}
	}

	return expiredCount
}

// GetMetadata returns metadata for all cached secrets
func (sc *SecretCache) GetMetadata() []SecretMetadata {
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	now := time.Now()
	metadata := make([]SecretMetadata, 0, len(sc.cache))

	for key, entry := range sc.cache {
		metadata = append(metadata, SecretMetadata{
			Key:          key,
			CreatedAt:    entry.CreatedAt,
			ExpiresAt:    entry.ExpiresAt,
			AccessCount:  entry.AccessCount,
			LastAccessed: entry.LastAccessed,
			IsExpired:    now.After(entry.ExpiresAt),
		})
	}

	return metadata
}

// Exists checks if a key exists in the cache (and is not expired)
func (sc *SecretCache) Exists(key string) bool {
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	entry, exists := sc.cache[key]
	if !exists {
		return false
	}

	return time.Now().Before(entry.ExpiresAt)
}

// Size returns the number of entries in the cache
func (sc *SecretCache) Size() int {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	return len(sc.cache)
}

// encrypt encrypts data using AES-GCM
func (sc *SecretCache) encrypt(data []byte) ([]byte, []byte, error) {
	block, err := aes.NewCipher(sc.encryptKey)
	if err != nil {
		return nil, nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}

	ciphertext := gcm.Seal(nil, nonce, data, nil)
	return ciphertext, nonce, nil
}

// decrypt decrypts data using AES-GCM
func (sc *SecretCache) decrypt(ciphertext, nonce []byte) ([]byte, error) {
	block, err := aes.NewCipher(sc.encryptKey)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}

	return plaintext, nil
}

// EncryptString encrypts a string and returns base64 encoded result
func EncryptString(data, password string) (string, error) {
	if password == "" {
		return "", errors.NewSecurityError("ENCRYPTION_ERROR", 
			"password cannot be empty", nil)
	}

	hash := sha256.Sum256([]byte(password))
	key := hash[:]

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(data), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// DecryptString decrypts a base64 encoded string
func DecryptString(encryptedData, password string) (string, error) {
	if password == "" {
		return "", errors.NewSecurityError("DECRYPTION_ERROR", 
			"password cannot be empty", nil)
	}

	data, err := base64.StdEncoding.DecodeString(encryptedData)
	if err != nil {
		return "", err
	}

	hash := sha256.Sum256([]byte(password))
	key := hash[:]

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.NewSecurityError("DECRYPTION_ERROR", 
			"encrypted data is too short", nil)
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}