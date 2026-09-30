//go:build !js

package pair

import (
	"crypto/ecdh"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// Secrets keeps the identity's private material. The app backs it with the
// desktop keyring; FileSecrets is the fallback and what tests use.
type Secrets interface {
	Get(name string) ([]byte, error) // ErrNoSecret when absent
	Set(name string, value []byte) error
}

// ErrNoSecret is returned by Secrets.Get for a name never set.
var ErrNoSecret = errors.New("pair: no such secret")

// Store is this device's identity and its paired peers. Peers live in
// peers.json (0600): they hold no private key, but an inbox secret lets
// its holder ring that device, so they are not world-readable either.
type Store struct {
	dir     string
	secrets Secrets

	mu    sync.Mutex
	id    *Identity
	peers []Peer
}

const secretName = "identity"

type identityFile struct {
	Key   []byte `json:"key"`
	Inbox []byte `json:"inbox"`
}

// Open loads (or creates) the store in dir.
func Open(dir string, secrets Secrets) (*Store, error) {
	s := &Store{dir: dir, secrets: secrets}
	b, err := os.ReadFile(s.peersPath())
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, &s.peers); err != nil {
			return nil, fmt.Errorf("pair: %s: %w", s.peersPath(), err)
		}
	}
	return s, nil
}

func (s *Store) peersPath() string { return filepath.Join(s.dir, "peers.json") }

// Identity returns this device's identity, creating it on first use.
func (s *Store) Identity() (Identity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.id != nil {
		return *s.id, nil
	}
	raw, err := s.secrets.Get(secretName)
	if errors.Is(err, ErrNoSecret) {
		id, err := NewIdentity()
		if err != nil {
			return Identity{}, err
		}
		b, _ := json.Marshal(identityFile{Key: id.Key.Bytes(), Inbox: id.Inbox})
		if err := s.secrets.Set(secretName, b); err != nil {
			return Identity{}, err
		}
		s.id = &id
		return id, nil
	}
	if err != nil {
		return Identity{}, err
	}
	var f identityFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return Identity{}, fmt.Errorf("pair: stored identity: %w", err)
	}
	key, err := ecdh.X25519().NewPrivateKey(f.Key)
	if err != nil || len(f.Inbox) != 32 {
		return Identity{}, errors.New("pair: stored identity is damaged")
	}
	s.id = &Identity{Key: key, Inbox: f.Inbox}
	return *s.id, nil
}

// Peers lists paired devices, oldest first.
func (s *Store) Peers() []Peer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.peers)
}

// Peer finds one by ID.
func (s *Store) Peer(id string) (Peer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.peers {
		if p.ID == id {
			return p, true
		}
	}
	return Peer{}, false
}

// Add saves a peer, replacing an earlier pairing with the same key.
func (s *Store) Add(p Peer) error {
	if len(p.Pub) != 32 || len(p.Inbox) != 32 {
		return errors.New("pair: incomplete peer")
	}
	p.ID = PeerID(p.Pub)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peers = slices.DeleteFunc(s.peers, func(q Peer) bool { return q.ID == p.ID })
	s.peers = append(s.peers, p)
	return s.saveLocked()
}

// Remove forgets a peer.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peers = slices.DeleteFunc(s.peers, func(q Peer) bool { return q.ID == id })
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.peers, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.peersPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.peersPath())
}

// FileSecrets keeps secrets as 0600 files in a directory.
type FileSecrets string

func (d FileSecrets) Get(name string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(string(d), name+".secret"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoSecret
	}
	return b, err
}

func (d FileSecrets) Set(name string, value []byte) error {
	if err := os.MkdirAll(string(d), 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(string(d), name+".secret"), value, 0o600)
}
