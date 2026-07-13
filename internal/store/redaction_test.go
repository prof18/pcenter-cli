package store

import (
	"strings"
	"testing"
)

func TestRedactorRemovesSecretsTokensAndSASQueries(t *testing.T) {
	t.Parallel()
	redactor := NewRedactor("client-secret", "access-token")
	input := "client_secret=client-secret Authorization: Bearer access-token upload=https://blob.example/file.zip?sv=1&sig=sas-secret"
	result := redactor.Redact(input)
	for _, secret := range []string{"client-secret", "access-token", "sas-secret", "sig="} {
		if strings.Contains(result, secret) {
			t.Fatalf("redacted output still contains %q: %s", secret, result)
		}
	}
	if !strings.Contains(result, "https://blob.example/file.zip?[REDACTED]") {
		t.Fatalf("SAS URL was not recognizably redacted: %s", result)
	}
}

func TestRedactJSONUploadURL(t *testing.T) {
	t.Parallel()
	input := []byte(`{"id":"one","fileUploadUrl":"https://blob.example/a?sig=secret","nested":{"fileUploadUrl":"https://blob.example/b?x=1"}}`)
	result, err := RedactUploadURLsJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	text := string(result)
	if strings.Contains(text, "secret") || strings.Contains(text, "?x=1") {
		t.Fatalf("upload URL query leaked: %s", text)
	}
	if strings.Count(text, "?[REDACTED]") != 2 {
		t.Fatalf("unexpected redaction: %s", text)
	}
}
