package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Simoon-F/aixvolink-pbx/internal/core/registration"
	"github.com/Simoon-F/aixvolink-pbx/internal/platform/memory"
	sipauth "github.com/Simoon-F/aixvolink-pbx/internal/sip/auth"
	"github.com/icholy/digest"
)

type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time { return c.now }

func TestAuthenticatorAcceptsHA1AndRejectsReplayAndStaleNonce(t *testing.T) {
	clock := &fixedClock{now: time.Date(2026, 6, 28, 8, 0, 0, 0, time.UTC)}
	credential := sipauth.Credential{
		TenantID: "tenant-1", Username: "1001", Realm: "example.invalid",
		HA1: sipauth.ComputeHA1("1001", "example.invalid", "test-password"), MaxBindings: 2,
	}
	authenticator, err := sipauth.New(sipauth.Config{
		Realm: credential.Realm, NonceSecret: []byte("0123456789abcdef0123456789abcdef"),
		NonceTTL: time.Minute, MaxReplayEntries: 8,
	}, memory.NewCredentialStore([]sipauth.Credential{credential}), clock)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	challengeValue, err := authenticator.Challenge(false)
	if err != nil {
		t.Fatalf("Challenge() error = %v", err)
	}
	challenge, err := digest.ParseChallenge(challengeValue)
	if err != nil {
		t.Fatalf("ParseChallenge() error = %v", err)
	}
	credentials, err := digest.Digest(challenge, digest.Options{
		Method: "REGISTER", URI: "sip:example.invalid", Username: "1001",
		Password: "test-password", Count: 1, Cnonce: "phase1-client",
	})
	if err != nil {
		t.Fatalf("Digest() error = %v", err)
	}

	got, err := authenticator.Verify(context.Background(), "REGISTER", "sip:example.invalid", credentials.String())
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if got.TenantID != registration.TenantID("tenant-1") {
		t.Fatalf("TenantID = %q", got.TenantID)
	}
	if _, err := authenticator.Verify(context.Background(), "REGISTER", "sip:example.invalid", credentials.String()); !errors.Is(err, sipauth.ErrReplay) {
		t.Fatalf("replay error = %v", err)
	}

	clock.now = clock.now.Add(2 * time.Minute)
	credentials.Nc = 2
	if _, err := authenticator.Verify(context.Background(), "REGISTER", "sip:example.invalid", credentials.String()); !errors.Is(err, sipauth.ErrStaleNonce) {
		t.Fatalf("stale error = %v", err)
	}
}

func TestAuthenticatorRejectsWrongResponseAndBoundsReplayCache(t *testing.T) {
	clock := &fixedClock{now: time.Date(2026, 6, 28, 8, 0, 0, 0, time.UTC)}
	credential := sipauth.Credential{
		TenantID: "tenant-1", Username: "1001", Realm: "example.invalid",
		HA1: sipauth.ComputeHA1("1001", "example.invalid", "correct"), MaxBindings: 1,
	}
	authenticator, err := sipauth.New(sipauth.Config{
		Realm: credential.Realm, NonceSecret: []byte("0123456789abcdef0123456789abcdef"),
		NonceTTL: time.Minute, MaxReplayEntries: 1,
	}, memory.NewCredentialStore([]sipauth.Credential{credential}), clock)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	for index, cnonce := range []string{"first", "second"} {
		challengeValue, err := authenticator.Challenge(false)
		if err != nil {
			t.Fatalf("Challenge() error = %v", err)
		}
		challenge, err := digest.ParseChallenge(challengeValue)
		if err != nil {
			t.Fatalf("ParseChallenge() error = %v", err)
		}
		credentials, err := digest.Digest(challenge, digest.Options{
			Method: "REGISTER", URI: "sip:example.invalid", Username: "1001",
			Password: "correct", Count: 1, Cnonce: cnonce,
		})
		if err != nil {
			t.Fatalf("Digest() error = %v", err)
		}
		_, verifyErr := authenticator.Verify(context.Background(), "REGISTER", "sip:example.invalid", credentials.String())
		if index == 0 && verifyErr != nil {
			t.Fatalf("first Verify() error = %v", verifyErr)
		}
		if index == 1 && !errors.Is(verifyErr, sipauth.ErrReplayCapacity) {
			t.Fatalf("second Verify() error = %v", verifyErr)
		}
	}
}
