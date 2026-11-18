package config_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/prof18/pcenter-cli/internal/config"
)

func TestLoadKeychainOnAnEmptyKeychainReturnsNothing(t *testing.T) {
	t.Parallel()
	values, err := config.LoadKeychain(config.NewMemoryKeychain())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(values) != 0 {
		t.Fatalf("values = %v, want empty", values)
	}
}

func TestSaveKeychainRoundTripsAndMerges(t *testing.T) {
	t.Parallel()
	keychain := config.NewMemoryKeychain()
	all := map[string]string{
		config.TenantIDName:     "tenant",
		config.ClientIDName:     "client",
		config.ClientSecretName: "secret",
		config.AppIDName:        "APP",
	}
	if err := config.SaveKeychain(keychain, all); err != nil {
		t.Fatalf("save: %v", err)
	}
	assertKeychainValues(t, keychain, all)

	// A later save of only the app id keeps the account credentials.
	if err := config.SaveKeychain(keychain, map[string]string{config.AppIDName: "OTHER"}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	all[config.AppIDName] = "OTHER"
	assertKeychainValues(t, keychain, all)

	// Blank values never overwrite stored ones.
	if err := config.SaveKeychain(keychain, map[string]string{config.TenantIDName: "  "}); err != nil {
		t.Fatalf("blank save: %v", err)
	}
	assertKeychainValues(t, keychain, all)
}

func TestLoadKeychainRejectsMalformedItems(t *testing.T) {
	t.Parallel()
	for _, item := range []string{"not json", "null", `{"tenantId":null}`, `{"clientId":5}`, `[]`} {
		keychain := config.NewMemoryKeychain()
		if err := keychain.Set(config.KeychainService, config.KeychainAccount, item); err != nil {
			t.Fatal(err)
		}
		_, err := config.LoadKeychain(keychain)
		var formatErr *config.KeychainFormatError
		if !errors.As(err, &formatErr) {
			t.Fatalf("item %q: err = %v, want *KeychainFormatError", item, err)
		}
		if !strings.Contains(err.Error(), config.KeychainService) {
			t.Fatalf("item %q: message %q does not name the service", item, err)
		}
	}
}

func TestKeychainFormatErrorNeverEchoesTheItem(t *testing.T) {
	t.Parallel()
	for _, item := range []string{`{"clientSecret":123456789e999}`, `{"clientSecret":"hunter2"`, `{"clientSecret":hunter2}`} {
		keychain := config.NewMemoryKeychain()
		if err := keychain.Set(config.KeychainService, config.KeychainAccount, item); err != nil {
			t.Fatal(err)
		}
		_, err := config.LoadKeychain(keychain)
		if err == nil || strings.Contains(err.Error(), "123456789") || strings.Contains(err.Error(), "hunter2") {
			t.Fatalf("item %q: error %v echoes the item", item, err)
		}
	}
}

func TestSaveKeychainReplacesAMalformedItem(t *testing.T) {
	t.Parallel()
	keychain := config.NewMemoryKeychain()
	if err := keychain.Set(config.KeychainService, config.KeychainAccount, "not json"); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{config.ClientSecretName: "secret"}
	if err := config.SaveKeychain(keychain, values); err != nil {
		t.Fatalf("save over malformed item: %v", err)
	}
	assertKeychainValues(t, keychain, values)
}

func TestSaveKeychainDetectsAnIgnoredWrite(t *testing.T) {
	t.Parallel()
	for name, values := range map[string]map[string]string{
		"values": {config.ClientSecretName: "secret"},
		"empty":  {},
	} {
		keychain := config.NewMemoryKeychain()
		keychain.IgnoreSet = true
		err := config.SaveKeychain(keychain, values)
		if err == nil || !strings.Contains(err.Error(), "not updated") {
			t.Fatalf("%s: err = %v, want a not updated error", name, err)
		}
	}
}

func TestDeleteKeychainReportsWhatItRemoved(t *testing.T) {
	t.Parallel()
	keychain := config.NewMemoryKeychain()
	if err := config.SaveKeychain(keychain, map[string]string{config.TenantIDName: "tenant"}); err != nil {
		t.Fatal(err)
	}
	removed, err := config.DeleteKeychain(keychain)
	if err != nil || !removed {
		t.Fatalf("first delete: removed=%v err=%v", removed, err)
	}
	removed, err = config.DeleteKeychain(keychain)
	if err != nil || removed {
		t.Fatalf("second delete: removed=%v err=%v", removed, err)
	}
}

func TestDeleteKeychainDetectsASurvivingItem(t *testing.T) {
	t.Parallel()
	keychain := config.NewMemoryKeychain()
	if err := config.SaveKeychain(keychain, map[string]string{config.TenantIDName: "tenant"}); err != nil {
		t.Fatal(err)
	}
	keychain.KeepOnDelete = true
	if _, err := config.DeleteKeychain(keychain); err == nil || !strings.Contains(err.Error(), "still present") {
		t.Fatalf("err = %v, want a still present error", err)
	}
}

func TestBypassKeychainAcceptsTruthyValuesOnly(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"1", "true", "TRUE", "yes", "on", " On "} {
		if !config.BypassKeychain(config.Environment{config.BypassKeychainEnv: value}) {
			t.Fatalf("%q should bypass", value)
		}
	}
	for _, value := range []string{"", "0", "false", "no"} {
		if config.BypassKeychain(config.Environment{config.BypassKeychainEnv: value}) {
			t.Fatalf("%q should not bypass", value)
		}
	}
}

func assertKeychainValues(t *testing.T, keychain config.Keychain, want map[string]string) {
	t.Helper()
	got, err := config.LoadKeychain(keychain)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("keychain = %v, want %v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("keychain[%s] = %q, want %q", key, got[key], value)
		}
	}
}
