package main

import (
	"encoding/base64"
	"errors"
	"log"
	"path/filepath"

	"github.com/zalando/go-keyring"

	"github.com/richardwooding/satchel/internal/pair"
)

// keyringService names satchel's entries in the desktop keyring.
func keyringService() string { return "satchel" + profileSuffix("-") }

// keyringSecrets keeps the device identity in the desktop keyring (Secret
// Service on Linux, Keychain on macOS, Credential Manager on Windows).
type keyringSecrets struct{}

func (keyringSecrets) Get(name string) ([]byte, error) {
	v, err := keyring.Get(keyringService(), name)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, pair.ErrNoSecret
	}
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(v)
}

func (keyringSecrets) Set(name string, value []byte) error {
	return keyring.Set(keyringService(), name, base64.StdEncoding.EncodeToString(value))
}

// openPairs opens the paired-device store, keeping the identity in the
// keyring when one answers and in a 0600 file beside the peers when not.
func openPairs(configDir string) (*pair.Store, error) {
	dir := filepath.Join(configDir, "satchel")
	if profileSuffix("") != "" {
		// A test profile never writes into the person's real keyring.
		return pair.Open(dir, pair.FileSecrets(filepath.Join(dir, "secrets")))
	}
	var secrets pair.Secrets = keyringSecrets{}
	if _, err := secrets.Get("identity"); err != nil && !errors.Is(err, pair.ErrNoSecret) {
		log.Printf("keyring unavailable (%v); keeping the device key in %s instead", err, dir)
		secrets = pair.FileSecrets(filepath.Join(dir, "secrets"))
	}
	return pair.Open(dir, secrets)
}
