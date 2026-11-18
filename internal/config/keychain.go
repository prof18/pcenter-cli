package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/zalando/go-keyring"
)

// Keychain is the system credential store. Code reaches it only through this
// interface so tests never touch the developer's real keychain; they use
// MemoryKeychain instead.
type Keychain interface {
	Get(service, account string) (string, error)
	Set(service, account, secret string) error
	Delete(service, account string) error
}

const (
	// KeychainService is the service name of pcenter's keychain item.
	KeychainService = "pcenter"
	// KeychainAccount is the account name of pcenter's keychain item. There is
	// one account today; a name per account leaves room for profiles later.
	KeychainAccount = "default"
	// BypassKeychainEnv turns the keychain off for every command when truthy.
	BypassKeychainEnv = "PCENTER_BYPASS_KEYCHAIN"
)

// ErrKeychainNotFound is returned by a Keychain when the item does not exist.
var ErrKeychainNotFound = errors.New("keychain item not found")

// keychainKeys maps the JSON keys of the keychain item to credential names.
var keychainKeys = []struct{ json, name string }{
	{"tenantId", TenantIDName},
	{"clientId", ClientIDName},
	{"clientSecret", ClientSecretName},
	{"appId", AppIDName},
}

type keychainItem struct {
	TenantID     string `json:"tenantId"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	AppID        string `json:"appId,omitempty"`
}

// KeychainFormatError is a keychain item that is not the JSON object pcenter
// writes. Resolution ignores such an item; login overwrites it.
type KeychainFormatError struct{ Err error }

func (e *KeychainFormatError) Error() string {
	return fmt.Sprintf("keychain item %s/%s is not valid JSON: %v", KeychainService, KeychainAccount, e.Err)
}

// Unwrap returns the decode error.
func (e *KeychainFormatError) Unwrap() error { return e.Err }

// LoadKeychain reads pcenter's keychain item as MS_STORE_* values. A missing
// item is an empty map, not an error.
func LoadKeychain(k Keychain) (map[string]string, error) {
	values, _, err := loadKeychainItem(k)
	return values, err
}

func loadKeychainItem(k Keychain) (values map[string]string, found bool, err error) {
	raw, err := k.Get(KeychainService, KeychainAccount)
	if errors.Is(err, ErrKeychainNotFound) {
		return map[string]string{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, true, &KeychainFormatError{Err: safeDecodeError(err)}
	}
	if decoded == nil {
		return nil, true, &KeychainFormatError{Err: errors.New("item is null")}
	}
	values = map[string]string{}
	for _, key := range keychainKeys {
		value, present := decoded[key.json]
		if !present {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return nil, true, &KeychainFormatError{Err: fmt.Errorf("%s is not a string", key.json)}
		}
		if text != "" {
			values[key.name] = text
		}
	}
	return values, true, nil
}

// SaveKeychain merges values into pcenter's keychain item. Blank values never
// replace stored ones, and a malformed item is replaced rather than merged.
func SaveKeychain(k Keychain, values map[string]string) error {
	merged, err := LoadKeychain(k)
	var formatErr *KeychainFormatError
	switch {
	case errors.As(err, &formatErr):
		merged = map[string]string{}
	case err != nil:
		return err
	}
	for key, value := range values {
		if strings.TrimSpace(value) != "" {
			merged[key] = value
		}
	}
	encoded, err := json.Marshal(keychainItem{
		TenantID:     merged[TenantIDName],
		ClientID:     merged[ClientIDName],
		ClientSecret: merged[ClientSecretName],
		AppID:        merged[AppIDName],
	})
	if err != nil {
		return fmt.Errorf("encode keychain item: %w", err)
	}
	if err := k.Set(KeychainService, KeychainAccount, string(encoded)); err != nil {
		return err
	}
	// On Linux, go-keyring returns nil when the user dismisses the Secret
	// Service prompt, so only reading the item back proves the write.
	stored, found, err := loadKeychainItem(k)
	if err != nil {
		return fmt.Errorf("read back keychain item: %w", err)
	}
	if !found || !sameValues(stored, merged) {
		return errors.New("keychain item was not updated")
	}
	return nil
}

// DeleteKeychain removes pcenter's keychain item and reports whether there was
// one. It confirms the item is gone, because a dismissed Secret Service prompt
// on Linux makes go-keyring report success without deleting anything.
func DeleteKeychain(k Keychain) (bool, error) {
	err := k.Delete(KeychainService, KeychainAccount)
	if errors.Is(err, ErrKeychainNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = k.Get(KeychainService, KeychainAccount)
	switch {
	case errors.Is(err, ErrKeychainNotFound):
		return true, nil
	case err != nil:
		return false, err
	}
	return false, errors.New("keychain item still present after delete")
}

// BypassKeychain reports whether PCENTER_BYPASS_KEYCHAIN asks pcenter to leave
// the keychain alone.
func BypassKeychain(environment Environment) bool {
	switch strings.ToLower(strings.TrimSpace(environment[BypassKeychainEnv])) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// safeDecodeError describes a JSON decode failure without quoting the item:
// encoding/json errors can echo raw tokens, and the item holds a secret.
func safeDecodeError(err error) error {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntaxErr):
		return fmt.Errorf("invalid JSON at byte %d", syntaxErr.Offset)
	case errors.As(err, &typeErr):
		if typeErr.Field != "" {
			return fmt.Errorf("field %s has the wrong type", typeErr.Field)
		}
		return fmt.Errorf("value at byte %d has the wrong type", typeErr.Offset)
	}
	return errors.New("invalid JSON")
}

func sameValues(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

// SystemKeychain is the operating system's credential store, through
// go-keyring: macOS Keychain, Windows Credential Manager, or Secret Service on
// Linux.
type SystemKeychain struct{}

// Get reads a secret, translating a missing item to ErrKeychainNotFound.
func (SystemKeychain) Get(service, account string) (string, error) {
	secret, err := keyring.Get(service, account)
	return secret, translateKeyringError(err)
}

// Set stores a secret.
func (SystemKeychain) Set(service, account, secret string) error {
	return translateKeyringError(keyring.Set(service, account, secret))
}

// Delete removes a secret, translating a missing item to ErrKeychainNotFound.
func (SystemKeychain) Delete(service, account string) error {
	return translateKeyringError(keyring.Delete(service, account))
}

func translateKeyringError(err error) error {
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrKeychainNotFound
	}
	return err
}

// MemoryKeychain is an in-memory Keychain for tests. Its exported fields
// simulate failures: Err fails every method, SetErr fails only Set, DeleteErr
// fails only Delete, KeepOnDelete makes Delete succeed without removing the
// item, and IgnoreSet makes Set succeed without storing anything.
type MemoryKeychain struct {
	Err          error
	SetErr       error
	DeleteErr    error
	KeepOnDelete bool
	IgnoreSet    bool

	mu    sync.Mutex
	items map[string]string
}

// NewMemoryKeychain returns an empty MemoryKeychain.
func NewMemoryKeychain() *MemoryKeychain {
	return &MemoryKeychain{items: map[string]string{}}
}

// Get returns a stored secret or ErrKeychainNotFound.
func (m *MemoryKeychain) Get(service, account string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return "", m.Err
	}
	secret, ok := m.items[service+"/"+account]
	if !ok {
		return "", ErrKeychainNotFound
	}
	return secret, nil
}

// Set stores a secret.
func (m *MemoryKeychain) Set(service, account, secret string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	if m.SetErr != nil {
		return m.SetErr
	}
	if !m.IgnoreSet {
		m.items[service+"/"+account] = secret
	}
	return nil
}

// Delete removes a secret or returns ErrKeychainNotFound.
func (m *MemoryKeychain) Delete(service, account string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	if m.DeleteErr != nil {
		return m.DeleteErr
	}
	key := service + "/" + account
	if _, ok := m.items[key]; !ok {
		return ErrKeychainNotFound
	}
	if !m.KeepOnDelete {
		delete(m.items, key)
	}
	return nil
}
