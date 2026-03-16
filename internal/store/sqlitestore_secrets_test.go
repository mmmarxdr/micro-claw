package store

import (
	"context"
	"errors"
	"os"
	"sort"
	"testing"

	"microagent/internal/config"
)

// testSecretsKey is a deterministic 32-byte (64 hex char) AES-256 key used in tests.
const testSecretsKey = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

// newSecretsStore creates a SQLiteStore with a temp directory and the test encryption key.
func newSecretsStore(t *testing.T) *SQLiteStore {
	t.Helper()
	st, err := NewSQLiteStore(config.StoreConfig{
		Type:          "sqlite",
		Path:          t.TempDir(),
		EncryptionKey: testSecretsKey,
	})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// newSecretsStoreNoKey creates a SQLiteStore with no encryption key configured.
func newSecretsStoreNoKey(t *testing.T) *SQLiteStore {
	t.Helper()
	st, err := NewSQLiteStore(config.StoreConfig{
		Type: "sqlite",
		Path: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewSQLiteStore (no key): %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// rawSecretValue reads the raw (encrypted) value column directly from the DB.
func rawSecretValue(t *testing.T, st *SQLiteStore, key string) string {
	t.Helper()
	var val string
	err := st.db.QueryRowContext(context.Background(),
		`SELECT value FROM secrets WHERE key = ?`, key,
	).Scan(&val)
	if err != nil {
		t.Fatalf("rawSecretValue(%q): %v", key, err)
	}
	return val
}

// ─── GetSecret ───────────────────────────────────────────────────────────────

// SC-GET-01: found → decrypted value
func TestGetSecret_Found(t *testing.T) {
	st := newSecretsStore(t)
	ctx := context.Background()

	if err := st.SetSecret(ctx, "mykey", "mysecret"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	got, err := st.GetSecret(ctx, "mykey")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if got != "mysecret" {
		t.Errorf("GetSecret = %q, want %q", got, "mysecret")
	}
}

// SC-GET-02: not found → ErrNotFound
func TestGetSecret_NotFound(t *testing.T) {
	st := newSecretsStore(t)
	ctx := context.Background()

	_, err := st.GetSecret(ctx, "missing")
	if err == nil {
		t.Fatal("GetSecret: expected error, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSecret error = %v, want wrapping ErrNotFound", err)
	}
}

// SC-GET-03: no encryption key → ErrEncryptionKeyNotConfigured
func TestGetSecret_NoEncryptionKey(t *testing.T) {
	st := newSecretsStoreNoKey(t)
	ctx := context.Background()

	_, err := st.GetSecret(ctx, "anykey")
	if err == nil {
		t.Fatal("GetSecret: expected error, got nil")
	}
	if !errors.Is(err, ErrEncryptionKeyNotConfigured) {
		t.Errorf("GetSecret error = %v, want ErrEncryptionKeyNotConfigured", err)
	}
}

// SC-GET-05: raw DB value is not the plaintext
func TestGetSecret_RawValueIsOpaque(t *testing.T) {
	st := newSecretsStore(t)
	ctx := context.Background()

	const plaintext = "supersecret"
	if err := st.SetSecret(ctx, "k", plaintext); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	raw := rawSecretValue(t, st, "k")
	if raw == plaintext {
		t.Errorf("raw DB value equals plaintext %q — secret is not encrypted", plaintext)
	}
}

// ─── SetSecret ───────────────────────────────────────────────────────────────

// SC-SET-01: insert → new secret is retrievable
func TestSetSecret_Insert(t *testing.T) {
	st := newSecretsStore(t)
	ctx := context.Background()

	if err := st.SetSecret(ctx, "newkey", "newvalue"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	got, err := st.GetSecret(ctx, "newkey")
	if err != nil {
		t.Fatalf("GetSecret after insert: %v", err)
	}
	if got != "newvalue" {
		t.Errorf("GetSecret = %q, want %q", got, "newvalue")
	}
}

// SC-SET-02: upsert → existing value is overwritten
func TestSetSecret_Upsert(t *testing.T) {
	st := newSecretsStore(t)
	ctx := context.Background()

	if err := st.SetSecret(ctx, "k", "v1"); err != nil {
		t.Fatalf("SetSecret v1: %v", err)
	}
	if err := st.SetSecret(ctx, "k", "v2"); err != nil {
		t.Fatalf("SetSecret v2: %v", err)
	}
	got, err := st.GetSecret(ctx, "k")
	if err != nil {
		t.Fatalf("GetSecret after upsert: %v", err)
	}
	if got != "v2" {
		t.Errorf("GetSecret = %q, want %q", got, "v2")
	}
}

// SC-SET-03: no encryption key → ErrEncryptionKeyNotConfigured
func TestSetSecret_NoEncryptionKey(t *testing.T) {
	st := newSecretsStoreNoKey(t)

	err := st.SetSecret(context.Background(), "k", "v")
	if err == nil {
		t.Fatal("SetSecret: expected error, got nil")
	}
	if !errors.Is(err, ErrEncryptionKeyNotConfigured) {
		t.Errorf("SetSecret error = %v, want ErrEncryptionKeyNotConfigured", err)
	}
}

// SC-SET-04: two writes of same key produce different ciphertexts (fresh nonce each time)
func TestSetSecret_FreshNoncePerWrite(t *testing.T) {
	st := newSecretsStore(t)
	ctx := context.Background()

	if err := st.SetSecret(ctx, "k", "v"); err != nil {
		t.Fatalf("SetSecret first write: %v", err)
	}
	raw1 := rawSecretValue(t, st, "k")

	if err := st.SetSecret(ctx, "k", "v"); err != nil {
		t.Fatalf("SetSecret second write: %v", err)
	}
	raw2 := rawSecretValue(t, st, "k")

	if raw1 == raw2 {
		t.Error("Both writes produced identical ciphertext — nonce was not randomised")
	}
}

// SC-SET-05: empty value is valid
func TestSetSecret_EmptyValue(t *testing.T) {
	st := newSecretsStore(t)
	ctx := context.Background()

	if err := st.SetSecret(ctx, "k", ""); err != nil {
		t.Fatalf("SetSecret empty value: %v", err)
	}
	got, err := st.GetSecret(ctx, "k")
	if err != nil {
		t.Fatalf("GetSecret after empty-value set: %v", err)
	}
	if got != "" {
		t.Errorf("GetSecret = %q, want empty string", got)
	}
}

// SC-SET-06: empty key is rejected
func TestSetSecret_EmptyKey(t *testing.T) {
	st := newSecretsStore(t)

	err := st.SetSecret(context.Background(), "", "value")
	if err == nil {
		t.Fatal("SetSecret empty key: expected error, got nil")
	}
}

// ─── DeleteSecret ────────────────────────────────────────────────────────────

// SC-DEL-01: existing key → removed and no longer retrievable
func TestDeleteSecret_ExistingKey(t *testing.T) {
	st := newSecretsStore(t)
	ctx := context.Background()

	if err := st.SetSecret(ctx, "k", "v"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	if err := st.DeleteSecret(ctx, "k"); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}
	_, err := st.GetSecret(ctx, "k")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSecret after delete: error = %v, want ErrNotFound", err)
	}
}

// SC-DEL-02: non-existent key → idempotent, no error
func TestDeleteSecret_NonExistentKey(t *testing.T) {
	st := newSecretsStore(t)

	if err := st.DeleteSecret(context.Background(), "missing"); err != nil {
		t.Errorf("DeleteSecret missing key: unexpected error: %v", err)
	}
}

// SC-DEL-03: delete does not affect other secrets
func TestDeleteSecret_DoesNotAffectOthers(t *testing.T) {
	st := newSecretsStore(t)
	ctx := context.Background()

	if err := st.SetSecret(ctx, "a", "va"); err != nil {
		t.Fatalf("SetSecret a: %v", err)
	}
	if err := st.SetSecret(ctx, "b", "vb"); err != nil {
		t.Fatalf("SetSecret b: %v", err)
	}
	if err := st.DeleteSecret(ctx, "a"); err != nil {
		t.Fatalf("DeleteSecret a: %v", err)
	}
	got, err := st.GetSecret(ctx, "b")
	if err != nil {
		t.Fatalf("GetSecret b after deleting a: %v", err)
	}
	if got != "vb" {
		t.Errorf("GetSecret b = %q, want %q", got, "vb")
	}
}

// ─── ListSecretKeys ──────────────────────────────────────────────────────────

// SC-LIST-01: returns all key names (no values)
func TestListSecretKeys_ReturnsAllKeys(t *testing.T) {
	st := newSecretsStore(t)
	ctx := context.Background()

	for _, k := range []string{"x", "y", "z"} {
		if err := st.SetSecret(ctx, k, "value-"+k); err != nil {
			t.Fatalf("SetSecret %q: %v", k, err)
		}
	}
	keys, err := st.ListSecretKeys(ctx)
	if err != nil {
		t.Fatalf("ListSecretKeys: %v", err)
	}
	sort.Strings(keys)
	want := []string{"x", "y", "z"}
	if len(keys) != len(want) {
		t.Fatalf("ListSecretKeys returned %v, want %v", keys, want)
	}
	for i, k := range keys {
		if k != want[i] {
			t.Errorf("keys[%d] = %q, want %q", i, k, want[i])
		}
	}
}

// SC-LIST-02: empty DB → empty non-nil slice
func TestListSecretKeys_EmptyDB(t *testing.T) {
	st := newSecretsStore(t)

	keys, err := st.ListSecretKeys(context.Background())
	if err != nil {
		t.Fatalf("ListSecretKeys: %v", err)
	}
	if keys == nil {
		t.Error("ListSecretKeys returned nil, want empty non-nil slice")
	}
	if len(keys) != 0 {
		t.Errorf("ListSecretKeys returned %v, want empty slice", keys)
	}
}

// SC-LIST-03: deleted keys do not appear
func TestListSecretKeys_DeletedKeysAbsent(t *testing.T) {
	st := newSecretsStore(t)
	ctx := context.Background()

	if err := st.SetSecret(ctx, "a", "va"); err != nil {
		t.Fatalf("SetSecret a: %v", err)
	}
	if err := st.SetSecret(ctx, "b", "vb"); err != nil {
		t.Fatalf("SetSecret b: %v", err)
	}
	if err := st.DeleteSecret(ctx, "a"); err != nil {
		t.Fatalf("DeleteSecret a: %v", err)
	}
	keys, err := st.ListSecretKeys(ctx)
	if err != nil {
		t.Fatalf("ListSecretKeys: %v", err)
	}
	if len(keys) != 1 || keys[0] != "b" {
		t.Errorf("ListSecretKeys = %v, want [b]", keys)
	}
}

// ─── Encryption Behaviour ────────────────────────────────────────────────────

// SC-ENC-01: wrong key → GetSecret returns decryption error
func TestGetSecret_WrongKey(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Write with key K1.
	st1, err := NewSQLiteStore(config.StoreConfig{
		Type:          "sqlite",
		Path:          dir,
		EncryptionKey: testSecretsKey,
	})
	if err != nil {
		t.Fatalf("NewSQLiteStore K1: %v", err)
	}
	if err := st1.SetSecret(ctx, "k", "secret"); err != nil {
		t.Fatalf("SetSecret K1: %v", err)
	}
	_ = st1.Close()

	// Open with different key K2.
	st2, err := NewSQLiteStore(config.StoreConfig{
		Type:          "sqlite",
		Path:          dir,
		EncryptionKey: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
	})
	if err != nil {
		t.Fatalf("NewSQLiteStore K2: %v", err)
	}
	defer st2.Close()

	_, err = st2.GetSecret(ctx, "k")
	if err == nil {
		t.Fatal("GetSecret with wrong key: expected error, got nil")
	}
}

// SC-ENC-02: encryption key from env var (config field empty)
func TestGetSecret_KeyFromEnvVar(t *testing.T) {
	t.Setenv("MICROAGENT_SECRET_KEY", testSecretsKey)

	st, err := NewSQLiteStore(config.StoreConfig{
		Type: "sqlite",
		Path: t.TempDir(),
		// EncryptionKey deliberately empty
	})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	if err := st.SetSecret(ctx, "envkey", "envval"); err != nil {
		t.Fatalf("SetSecret via env key: %v", err)
	}
	got, err := st.GetSecret(ctx, "envkey")
	if err != nil {
		t.Fatalf("GetSecret via env key: %v", err)
	}
	if got != "envval" {
		t.Errorf("GetSecret = %q, want %q", got, "envval")
	}
}

// SC-ENC-03: invalid hex key → error on first use
func TestSetSecret_InvalidHexKey(t *testing.T) {
	st, err := NewSQLiteStore(config.StoreConfig{
		Type:          "sqlite",
		Path:          t.TempDir(),
		EncryptionKey: "notvalidhex!!!",
	})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	err = st.SetSecret(context.Background(), "k", "v")
	if err == nil {
		t.Fatal("SetSecret with invalid hex key: expected error, got nil")
	}
}

// ─── Interface assertion (runtime) ───────────────────────────────────────────

// TestSQLiteStore_ImplementsSecretsStore ensures the type-assertion works at runtime.
func TestSQLiteStore_ImplementsSecretsStore(t *testing.T) {
	st := newSecretsStore(t)
	var iface any = st
	if _, ok := iface.(SecretsStore); !ok {
		t.Error("SQLiteStore does not implement SecretsStore at runtime")
	}
}

// ─── Verify env var cleanup doesn't leak between tests ───────────────────────

// TestGetSecret_NoKeyAfterEnvCleared verifies no env var leaks from other tests.
func TestGetSecret_NoKeyAfterEnvCleared(t *testing.T) {
	// Ensure env var is unset (t.Setenv in other tests is automatically restored).
	if v := os.Getenv("MICROAGENT_SECRET_KEY"); v != "" {
		t.Skipf("MICROAGENT_SECRET_KEY is set in environment (%q) — skipping leakage test", v)
	}
	st := newSecretsStoreNoKey(t)

	_, err := st.GetSecret(context.Background(), "k")
	if !errors.Is(err, ErrEncryptionKeyNotConfigured) {
		t.Errorf("GetSecret without any key = %v, want ErrEncryptionKeyNotConfigured", err)
	}
}

// ─── updated_at sanity (SC-SET-07, best-effort) ─────────────────────────────

func TestSetSecret_UpdatedAtAdvances(t *testing.T) {
	st := newSecretsStore(t)
	ctx := context.Background()

	if err := st.SetSecret(ctx, "ts", "v1"); err != nil {
		t.Fatalf("SetSecret v1: %v", err)
	}
	var t1 string
	if err := st.db.QueryRowContext(ctx, `SELECT updated_at FROM secrets WHERE key = ?`, "ts").Scan(&t1); err != nil {
		t.Fatalf("reading updated_at v1: %v", err)
	}

	// A tiny sleep is avoided: SQLite stores with second-level precision via TEXT.
	// We just verify the column is not empty and is a non-blank string.
	if t1 == "" {
		t.Error("updated_at is empty after first SetSecret")
	}

	if err := st.SetSecret(ctx, "ts", "v2"); err != nil {
		t.Fatalf("SetSecret v2: %v", err)
	}
	var t2 string
	if err := st.db.QueryRowContext(ctx, `SELECT updated_at FROM secrets WHERE key = ?`, "ts").Scan(&t2); err != nil {
		t.Fatalf("reading updated_at v2: %v", err)
	}
	if t2 == "" {
		t.Error("updated_at is empty after second SetSecret")
	}
	// Both should be non-empty; we can't guarantee t2 > t1 within the same nanosecond,
	// but we can assert t2 >= t1.
	if t2 < t1 {
		t.Errorf("updated_at went backwards: t1=%q t2=%q", t1, t2)
	}
}
