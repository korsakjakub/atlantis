package oidc

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// Identity is the authenticated user taken from a verified ID token.
type Identity struct {
	Subject string
	Email   string
	Name    string
	Groups  []string
	Issuer  string
	Preset  string
}

type session struct {
	identity Identity
	expires  time.Time
}

// pending is an in-flight login, keyed by the state value.
type pending struct {
	nonce    string
	verifier string
	next     string
	expires  time.Time
}

// store keeps sessions and pending logins in memory. Sessions do not survive
// a restart and are not shared between replicas.
type store struct {
	mu       sync.Mutex
	now      func() time.Time
	sessions map[string]session
	pendings map[string]pending
}

func newStore(now func() time.Time) *store {
	return &store{now: now, sessions: map[string]session{}, pendings: map[string]pending{}}
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// gc must be called with mu held.
func (s *store) gc() {
	t := s.now()
	for k, v := range s.sessions {
		if !t.Before(v.expires) {
			delete(s.sessions, k)
		}
	}
	for k, v := range s.pendings {
		if !t.Before(v.expires) {
			delete(s.pendings, k)
		}
	}
}

func (s *store) putPending(state string, p pending) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gc()
	s.pendings[state] = p
}

// takePending returns and deletes the pending login, so a state value can be
// used only once.
func (s *store) takePending(state string) (pending, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pendings[state]
	delete(s.pendings, state)
	if !ok || !s.now().Before(p.expires) {
		return pending{}, false
	}
	return p, true
}

func (s *store) createSession(id Identity, ttl time.Duration) (string, time.Time, error) {
	tok, err := randomToken()
	if err != nil {
		return "", time.Time{}, err
	}
	exp := s.now().Add(ttl)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gc()
	s.sessions[tok] = session{identity: id, expires: exp}
	return tok, exp, nil
}

func (s *store) lookup(tok string) (Identity, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[tok]
	if !ok {
		return Identity{}, false
	}
	if !s.now().Before(sess.expires) {
		delete(s.sessions, tok)
		return Identity{}, false
	}
	return sess.identity, true
}

func (s *store) revoke(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, tok)
}
