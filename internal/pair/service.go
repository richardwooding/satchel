package pair

import (
	"fmt"
	"strings"
	"sync"
	"unicode"

	"github.com/richardwooding/parley/service"
	"github.com/richardwooding/parley/session"
	"github.com/richardwooding/parley/wire"
)

// ServiceID is the parley service that swaps identities while pairing.
const ServiceID = "pair"

const maxName = 40

// hello is what each end tells the other: who it is and how to ring it.
type hello struct {
	Name  string `cbor:"1,keyasint"`
	Pub   []byte `cbor:"2,keyasint"`
	Inbox []byte `cbor:"3,keyasint"`
}

// Candidate is emitted when another member of the pairing session has
// introduced itself. It is not saved until the person confirms the code.
type Candidate struct {
	From wire.ParticipantID
	Peer Peer
	Code string
}

// Service runs on a pairing session. Both ends introduce themselves; each
// sees the other as a Candidate with the check code to compare.
type Service struct {
	service.Base
	id   Identity
	name string

	mu   sync.Mutex
	told map[wire.ParticipantID]bool
}

// NewService returns the pairing service for this identity and device name.
func NewService(id Identity, name string) *Service {
	return &Service{id: id, name: cleanName(name), told: map[wire.ParticipantID]bool{}}
}

func (s *Service) ID() string   { return ServiceID }
func (s *Service) Version() int { return 1 }

// Attach introduces a joiner to everyone already there; the host answers
// each introduction with its own.
func (s *Service) Attach(ctx service.Context) {
	s.SetContext(ctx)
	if !ctx.Host {
		if b, err := wire.Marshal(s.hello()); err == nil {
			_ = ctx.Send.Broadcast(ServiceID, b)
		}
	}
}

func (s *Service) hello() hello {
	return hello{Name: s.name, Pub: s.id.Public(), Inbox: s.id.Inbox}
}

func (s *Service) HandleFrame(from wire.ParticipantID, body []byte) error {
	h, err := wire.Body[hello](body)
	if err != nil {
		return fmt.Errorf("pair: %w", err)
	}
	if len(h.Pub) != 32 || len(h.Inbox) != 32 {
		return fmt.Errorf("pair: malformed hello from %d", from)
	}
	secret, err := s.id.Secret(h.Pub)
	if err != nil {
		return err
	}
	s.mu.Lock()
	reply := !s.told[from]
	s.told[from] = true
	s.mu.Unlock()
	if reply {
		if b, err := wire.Marshal(s.hello()); err == nil {
			_ = s.Ctx().Send.SendTo(from, ServiceID, b)
		}
	}
	s.Ctx().Emit(Candidate{
		From: from,
		Peer: Peer{ID: PeerID(h.Pub), Name: cleanName(h.Name), Pub: h.Pub, Inbox: h.Inbox},
		Code: CheckCode(secret),
	})
	return nil
}

func (s *Service) Snapshot() ([]byte, error) { return nil, nil }
func (s *Service) Restore([]byte) error      { return nil }

// MemberKeyed and MemberLeft satisfy service.MemberObserver; a leaver who
// rejoins introduces itself again.
func (s *Service) MemberKeyed(wire.ParticipantID, session.Role) {}
func (s *Service) MemberLeft(id wire.ParticipantID) {
	s.mu.Lock()
	delete(s.told, id)
	s.mu.Unlock()
}

// cleanName keeps a device name printable and short; it is only ever shown.
func cleanName(n string) string {
	n = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, strings.TrimSpace(n))
	if r := []rune(n); len(r) > maxName {
		n = string(r[:maxName])
	}
	if n == "" {
		n = "unnamed device"
	}
	return n
}
