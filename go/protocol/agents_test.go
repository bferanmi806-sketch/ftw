package protocol

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
)

// The approval vectors are what the phone and FTW Cloud replay: for each
// signed message, its body as it travels, the exact bytes the phone signs
// and a signature by the vector key. Regenerate with
// FTW_WRITE_VECTORS=1 go test ./protocol -run TestApprovalVectors; the
// bytes must not change for an existing case.

type approvalVectors struct {
	Note     string           `json:"note"`
	PubHex   string           `json:"pubHex"`
	BoxKey   string           `json:"boxKeyHex"`
	DeviceID string           `json:"deviceId"`
	Cases    []approvalVector `json:"cases"`
}

type approvalVector struct {
	What      string `json:"what"`
	BodyHex   string `json:"bodyHex"`
	SignedHex string `json:"signedHex"`
	SHA256Hex string `json:"sha256Hex"`
	SigHex    string `json:"sigHex"`
}

const approvalVectorsPath = "testdata/approval-vectors.json"

// vectorKey is a fixed P-256 key, so the public half in the file never
// changes. It signs nothing outside these tests.
func vectorKey() *ecdsa.PrivateKey {
	d, _ := new(big.Int).SetString("6b2c7e1f0a9d3c4b5e8f7a6d1c2b3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d", 16)
	key := &ecdsa.PrivateKey{D: d}
	key.Curve = elliptic.P256()
	key.X, key.Y = key.Curve.ScalarBaseMult(d.Bytes())
	return key
}

func vectorPub(key *ecdsa.PrivateKey) []byte {
	return elliptic.Marshal(elliptic.P256(), key.X, key.Y) //nolint:staticcheck // SEC1 uncompressed is the wire form.
}

func signRaw(t *testing.T, key *ecdsa.PrivateKey, msg []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(msg)
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, ApprovalSigBytes)
	r.FillBytes(out[:32])
	s.FillBytes(out[32:])
	return out
}

func vectorFor() SignedFor {
	box := make([]byte, 32)
	for i := range box {
		box[i] = byte(i + 1)
	}
	return SignedFor{BoxKey: box, DeviceID: "ownerPh1"}
}

func vectorAgentKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(0xa0 + i)
	}
	return key
}

// vectorMessages builds every signed message, each with its signing bytes.
func vectorMessages(t *testing.T, key *ecdsa.PrivateKey) []struct {
	what string
	body any
	sign func(sig []byte) any
	raw  []byte
} {
	t.Helper()
	f := vectorFor()
	const atMs = 1_792_000_000_000

	keySet := ApprovalKeySet{Pub: vectorPub(key), AtMs: atMs}
	add := AgentAdd{
		Key: vectorAgentKey(), Role: RoleAgent,
		Scopes:      []string{ScopeDispatchHold, ScopeLiveRead, ScopePlanRead},
		Ops:         []string{OpLoadpointHold},
		Caps:        Caps{HoldS: 900, MaxW: 11_000, PerMin: 6},
		ExpiresAtMs: atMs + AgentMaxMs,
		Principal:   Principal{Client: "Claude", ClientID: "https://claude.ai/oauth/mcp-client-metadata"},
		AtMs:        atMs,
	}
	support := AgentAdd{
		Key: vectorAgentKey(), Role: RoleSupport,
		Scopes:      []string{ScopeHealthRead, ScopeLogsRead},
		ExpiresAtMs: atMs + SupportMaxMs,
		Principal:   Principal{Client: "Sourceful support", Ticket: "FTW-123"},
		AtMs:        atMs,
	}
	widen := AgentWiden{
		ID: "oKGio6Sl", Epoch: 2,
		Scopes:      []string{ScopeLiveRead, ScopePlanRead},
		Caps:        Caps{PerMin: 6},
		ExpiresAtMs: atMs + AgentMaxMs, AtMs: atMs,
	}
	confirm := AgentConfirm{ReqID: "r-2f1e0d", Approve: true, AtMs: atMs}

	keySetRaw, err := KeySetBytes(f, keySet)
	if err != nil {
		t.Fatal(err)
	}
	addRaw, err := AddBytes(f, add)
	if err != nil {
		t.Fatal(err)
	}
	supportRaw, err := AddBytes(f, support)
	if err != nil {
		t.Fatal(err)
	}
	widenRaw, err := WidenBytes(f, widen)
	if err != nil {
		t.Fatal(err)
	}
	confirmRaw, err := ConfirmBytes(f, confirm)
	if err != nil {
		t.Fatal(err)
	}
	type msg = struct {
		what string
		body any
		sign func(sig []byte) any
		raw  []byte
	}
	return []msg{
		{MsgApprovalKeySet, keySet, func(sig []byte) any { m := keySet; m.Sig = sig; return m }, keySetRaw},
		{MsgAgentAdd, add, func(sig []byte) any { m := add; m.Sig = sig; return m }, addRaw},
		{MsgAgentAdd, support, func(sig []byte) any { m := support; m.Sig = sig; return m }, supportRaw},
		{MsgAgentWiden, widen, func(sig []byte) any { m := widen; m.Sig = sig; return m }, widenRaw},
		{MsgAgentConfirm, confirm, func(sig []byte) any { m := confirm; m.Sig = sig; return m }, confirmRaw},
	}
}

func TestApprovalVectors(t *testing.T) {
	key := vectorKey()
	msgs := vectorMessages(t, key)

	if os.Getenv("FTW_WRITE_VECTORS") != "" {
		f := vectorFor()
		out := approvalVectors{
			Note: "docs/agents.md, The approval signature. signedHex is what the phone signs; " +
				"sigHex is ECDSA P-256 over SHA-256 of it, r||s.",
			PubHex: hex.EncodeToString(vectorPub(key)), BoxKey: hex.EncodeToString(f.BoxKey), DeviceID: f.DeviceID,
		}
		for _, m := range msgs {
			sig := signRaw(t, key, m.raw)
			body, err := Marshal(m.sign(sig))
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(m.raw)
			out.Cases = append(out.Cases, approvalVector{
				What: m.what, BodyHex: hex.EncodeToString(body), SignedHex: hex.EncodeToString(m.raw),
				SHA256Hex: hex.EncodeToString(sum[:]), SigHex: hex.EncodeToString(sig),
			})
		}
		raw, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.FromSlash(approvalVectorsPath), append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	raw, err := os.ReadFile(filepath.FromSlash(approvalVectorsPath))
	if err != nil {
		t.Fatal(err)
	}
	var vec approvalVectors
	if err := json.Unmarshal(raw, &vec); err != nil {
		t.Fatal(err)
	}
	if vec.PubHex != hex.EncodeToString(vectorPub(key)) {
		t.Fatal("the vector key changed")
	}
	if len(vec.Cases) != len(msgs) {
		t.Fatalf("%d cases, want %d", len(vec.Cases), len(msgs))
	}
	pub := vectorPub(key)
	f := SignedFor{DeviceID: vec.DeviceID}
	f.BoxKey, _ = hex.DecodeString(vec.BoxKey)
	for i, c := range vec.Cases {
		if c.What != msgs[i].what {
			t.Fatalf("case %d is %s, want %s", i, c.What, msgs[i].what)
		}
		if got := hex.EncodeToString(msgs[i].raw); got != c.SignedHex {
			t.Errorf("case %d (%s): signed bytes changed\n got %s\nwant %s", i, c.What, got, c.SignedHex)
		}
		// Rebuild the signed bytes from the body as it travels, which is
		// what the box does.
		body, _ := hex.DecodeString(c.BodyHex)
		signed, sig := rebuild(t, c.What, f, body)
		if hex.EncodeToString(signed) != c.SignedHex {
			t.Errorf("case %d (%s): the body does not rebuild the signed bytes", i, c.What)
		}
		if hex.EncodeToString(sig) != c.SigHex {
			t.Errorf("case %d (%s): the body carries another signature", i, c.What)
		}
		if err := VerifyApproval(pub, signed, sig); err != nil {
			t.Errorf("case %d (%s): %v", i, c.What, err)
		}
		sum := sha256.Sum256(signed)
		if hex.EncodeToString(sum[:]) != c.SHA256Hex {
			t.Errorf("case %d (%s): digest", i, c.What)
		}
	}
}

func rebuild(t *testing.T, what string, f SignedFor, body []byte) ([]byte, []byte) {
	t.Helper()
	var signed []byte
	var sig []byte
	var err error
	switch what {
	case MsgApprovalKeySet:
		var m ApprovalKeySet
		if err = Unmarshal(body, &m); err == nil {
			signed, err = KeySetBytes(f, m)
			sig = m.Sig
		}
	case MsgAgentAdd:
		var m AgentAdd
		if err = Unmarshal(body, &m); err == nil {
			signed, err = AddBytes(f, m)
			sig = m.Sig
		}
	case MsgAgentWiden:
		var m AgentWiden
		if err = Unmarshal(body, &m); err == nil {
			signed, err = WidenBytes(f, m)
			sig = m.Sig
		}
	case MsgAgentConfirm:
		var m AgentConfirm
		if err = Unmarshal(body, &m); err == nil {
			signed, err = ConfirmBytes(f, m)
			sig = m.Sig
		}
	default:
		t.Fatalf("no rebuild for %s", what)
	}
	if err != nil {
		t.Fatal(err)
	}
	return signed, sig
}

func TestApprovalRefusesWhatItDidNotSign(t *testing.T) {
	key := vectorKey()
	pub := vectorPub(key)
	f := vectorFor()
	add := AgentAdd{Key: vectorAgentKey(), Role: RoleAgent, Scopes: []string{ScopeLiveRead},
		ExpiresAtMs: 2_000, AtMs: 1_000}
	raw, err := AddBytes(f, add)
	if err != nil {
		t.Fatal(err)
	}
	sig := signRaw(t, key, raw)
	if err := VerifyApproval(pub, raw, sig); err != nil {
		t.Fatal(err)
	}

	wider := add
	wider.Scopes = []string{ScopeDispatchHold, ScopeLiveRead}
	widerRaw, _ := AddBytes(f, wider)
	if !errors.Is(VerifyApproval(pub, widerRaw, sig), ErrApprovalSignature) {
		t.Error("a signature over read access passed for control")
	}

	other := f
	other.BoxKey = bytes.Repeat([]byte{9}, 32)
	otherRaw, _ := AddBytes(other, add)
	if !errors.Is(VerifyApproval(pub, otherRaw, sig), ErrApprovalSignature) {
		t.Error("a signature for one box passed at another")
	}

	if !errors.Is(VerifyApproval(pub, raw, sig[:63]), ErrApprovalSignature) {
		t.Error("a short signature passed")
	}
	if !errors.Is(VerifyApproval(pub[:64], raw, sig), ErrApprovalPub) {
		t.Error("a short key passed")
	}
	bad := append([]byte(nil), pub...)
	bad[64] ^= 1
	if !errors.Is(VerifyApproval(bad, raw, sig), ErrApprovalPub) {
		t.Error("a point off the curve passed")
	}
}

func TestApprovalBytesRefuseAnUnsortedList(t *testing.T) {
	f := vectorFor()
	for _, scopes := range [][]string{
		{ScopePlanRead, ScopeLiveRead},
		{ScopeLiveRead, ScopeLiveRead},
	} {
		_, err := AddBytes(f, AgentAdd{Key: vectorAgentKey(), Role: RoleAgent, Scopes: scopes})
		if !errors.Is(err, ErrApprovalUnsorted) {
			t.Errorf("%v: %v", scopes, err)
		}
	}
}

func TestEnvelopeCarriesByOnlyWhenSet(t *testing.T) {
	id := uint32(7)
	plain, err := EncodeEnvelope(Envelope{T: MsgPlanGet, ID: &id})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(plain, []byte("by")) {
		t.Error("an envelope without a staff id names one")
	}
	withBy, err := EncodeEnvelope(Envelope{T: MsgPlanGet, ID: &id, By: "staff-42"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeEnvelope(withBy)
	if err != nil || got.By != "staff-42" {
		t.Fatalf("by did not survive: %+v %v", got, err)
	}
}
