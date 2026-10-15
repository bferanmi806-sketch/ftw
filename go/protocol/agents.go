package protocol

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"slices"
)

// Agents: grants an owner gives to an assistant, to Sourceful support for one
// ticket, or to the release check. docs/agents.md is the normative text; this
// file holds the shapes and the approval signature's bytes, so FTW Cloud and
// the phone build them from the same code the box checks them with.

// Agent message types. Each request gets MsgAgentOK or MsgError with its id.
const (
	MsgApprovalKeySet = "approval.key.set"
	MsgAgentAdd       = "agent.add"
	MsgAgentWiden     = "agent.widen"
	MsgAgentRevoke    = "agent.revoke"
	MsgAgentList      = "agent.list"
	MsgAgentRequest   = "agent.request"
	MsgAgentConfirm   = "agent.confirm"
	MsgAgentOK        = "agent.ok"
	MsgAuditGet       = "audit.get"

	MsgHealthGet = "health.get"
	MsgHealth    = "health"
	MsgEventsGet = "events.get"
	MsgEvents    = "events"
	MsgLogsGet   = "logs.get"
	MsgLogs      = "logs"
	MsgConfigGet = "config.get"
	MsgConfig    = "config"
	MsgDiagRun   = "diag.run"
	MsgDiag      = "diag"
)

// TerminateExpired ends an agent's session when its grant runs out.
const TerminateExpired = "expired"

// Request states.
const (
	RequestPending  = "pending"
	RequestApproved = "approved"
	RequestDeclined = "declined"
	RequestExpired  = "expired"
)

// Limits the box enforces on every grant, whatever the grant says.
const (
	// AgentMaxMs, SupportMaxMs and ReleaseMaxMs are the longest grants.
	AgentMaxMs   int64 = 30 * 24 * 60 * 60 * 1000
	SupportMaxMs int64 = 14 * 24 * 60 * 60 * 1000
	ReleaseMaxMs int64 = 90 * 24 * 60 * 60 * 1000

	// AgentRowsMax is how many agent rows one box holds.
	AgentRowsMax = 16

	HoldSMin  = 60
	HoldSMax  = 1800
	MaxWMin   = 1
	MaxWMax   = 100_000
	PerMinMin = 1
	PerMinMax = 60
	// PerMinDefault is the rate a grant that names none gets.
	PerMinDefault = 6
	// ReadsPerMin is the box's own limit on an agent's reads.
	ReadsPerMin = 60

	// PendingPerAgent and PendingPerBox bound the requests waiting for an
	// owner. A request lives RequestTTLMs and is kept RequestKeptMs after
	// it ends, so the agent can read how it ended.
	PendingPerAgent       = 4
	PendingPerBox         = 16
	RequestTTLMs    int64 = 12 * 60 * 60 * 1000
	RequestKeptMs   int64 = 24 * 60 * 60 * 1000
	RequestArgsMax        = 1024
	RequestWhyMax         = 280

	// ApprovalSkewMs is how far an approval's atMs may stray from the box's
	// wall clock, and ApprovalReplayMs how long the box remembers one it took.
	ApprovalSkewMs   int64 = 10 * 60 * 1000
	ApprovalReplayMs int64 = 20 * 60 * 1000

	// LogsLinesMax bounds logs.get; the answer also fits one bulk frame.
	LogsLinesMax  = 200
	EventsMax     = 50
	AuditPageMax  = 200
	AuditRowsKept = 10_000
	AuditDaysKept = 90
	// ByMax bounds the staff id a support call carries.
	ByMax = 64
	// PrincipalFieldMax bounds each principal field.
	PrincipalFieldMax = 256
)

// Caps bound what one grant may do.
type Caps struct {
	// HoldS is the longest hold, in seconds.
	HoldS uint64 `cbor:"holdS" json:"holdS"`
	// MaxW is the largest power a hold may set, in W.
	MaxW uint64 `cbor:"maxW" json:"maxW"`
	// PerMin is cmd and agent.request per rolling minute.
	PerMin uint64 `cbor:"perMin" json:"perMin"`
}

// Principal says who an agent row is for. Display and audit only: the role
// decides what it may do.
type Principal struct {
	Client   string `cbor:"client" json:"client"`
	ClientID string `cbor:"clientId" json:"clientId"`
	Ticket   string `cbor:"ticket" json:"ticket"`
}

// AgentRow is one agent grant as the box holds it.
type AgentRow struct {
	ID          string    `cbor:"id" json:"id"`
	Key         []byte    `cbor:"key" json:"key"`
	Role        string    `cbor:"role" json:"role"`
	Scopes      []string  `cbor:"scopes" json:"scopes"`
	Ops         []string  `cbor:"ops" json:"ops"`
	Caps        Caps      `cbor:"caps" json:"caps"`
	ExpiresAtMs int64     `cbor:"expiresAtMs" json:"expiresAtMs"`
	Principal   Principal `cbor:"principal" json:"principal"`
	ApprovedBy  string    `cbor:"approvedBy" json:"approvedBy"`
	AddedAtMs   int64     `cbor:"addedAtMs" json:"addedAtMs"`
	LastSeenMs  int64     `cbor:"lastSeenMs" json:"lastSeenMs"`
	Epoch       uint64    `cbor:"epoch" json:"epoch"`
}

// ApprovalKeySet registers an owner phone's approval key.
type ApprovalKeySet struct {
	Pub  []byte `cbor:"pub"`
	AtMs int64  `cbor:"atMs"`
	Sig  []byte `cbor:"sig"`
}

// AgentAdd grants access.
type AgentAdd struct {
	Key         []byte    `cbor:"key"`
	Role        string    `cbor:"role"`
	Scopes      []string  `cbor:"scopes"`
	Ops         []string  `cbor:"ops"`
	Caps        Caps      `cbor:"caps"`
	ExpiresAtMs int64     `cbor:"expiresAtMs"`
	Principal   Principal `cbor:"principal"`
	AtMs        int64     `cbor:"atMs"`
	Sig         []byte    `cbor:"sig"`
}

// AgentWiden replaces a grant's rights. Any change but a revoke is signed.
type AgentWiden struct {
	ID          string   `cbor:"id"`
	Epoch       uint64   `cbor:"epoch"`
	Scopes      []string `cbor:"scopes"`
	Ops         []string `cbor:"ops"`
	Caps        Caps     `cbor:"caps"`
	ExpiresAtMs int64    `cbor:"expiresAtMs"`
	AtMs        int64    `cbor:"atMs"`
	Sig         []byte   `cbor:"sig"`
}

// GrantOffer is what FTW Cloud's consent page offers the owner: the agent's
// key and the rights its client asks for (docs/agents.md, The grant offer).
// It travels as CBOR, base64url without padding, in a link's fragment. The
// owner may grant less than it asks.
type GrantOffer struct {
	V        uint64   `cbor:"v"`
	Home     string   `cbor:"home"`
	Key      []byte   `cbor:"key"`
	Role     string   `cbor:"role"`
	Client   string   `cbor:"client"`
	ClientID string   `cbor:"clientId"`
	Scopes   []string `cbor:"scopes"`
	Ops      []string `cbor:"ops"`
	Caps     Caps     `cbor:"caps"`
	TTLS     uint64   `cbor:"ttlS"`
	Ticket   string   `cbor:"ticket,omitempty"`
	Code     string   `cbor:"code"`
}

// GrantOfferVersion is GrantOffer.V.
const GrantOfferVersion = 1

// AgentRevoke removes a grant. No signature.
type AgentRevoke struct {
	ID string `cbor:"id"`
}

// AgentRequest is an agent asking for a durable change.
type AgentRequest struct {
	Op   string         `cbor:"op"`
	Args map[string]any `cbor:"args"`
	Why  string         `cbor:"why"`
}

// AgentConfirm answers a request. Approving is signed; declining is not.
type AgentConfirm struct {
	ReqID   string `cbor:"reqId"`
	Approve bool   `cbor:"approve"`
	AtMs    int64  `cbor:"atMs,omitempty"`
	Sig     []byte `cbor:"sig,omitempty"`
}

// Request is one durable change an agent asked for.
type Request struct {
	ReqID       string         `cbor:"reqId" json:"reqId"`
	Agent       string         `cbor:"agent" json:"agent"`
	Op          string         `cbor:"op" json:"op"`
	Args        map[string]any `cbor:"args" json:"args"`
	Why         string         `cbor:"why" json:"why"`
	AtMs        int64          `cbor:"atMs" json:"atMs"`
	ExpiresAtMs int64          `cbor:"expiresAtMs" json:"expiresAtMs"`
	State       string         `cbor:"state" json:"state"`
}

// ApprovalKeyInfo is one owner phone's registered approval key.
type ApprovalKeyInfo struct {
	DeviceID string `cbor:"deviceId" json:"deviceId"`
	SetAtMs  int64  `cbor:"setAtMs" json:"setAtMs"`
}

// AgentList is agent.ok's body for agent.list. An agent sees its own row.
type AgentList struct {
	Agents   []AgentRow        `cbor:"agents" json:"agents"`
	Requests []Request         `cbor:"requests" json:"requests"`
	Keys     []ApprovalKeyInfo `cbor:"keys" json:"keys"`
}

// AuditGet pages through the audit log, newest first.
type AuditGet struct {
	BeforeSeq uint64 `cbor:"beforeSeq,omitempty"`
	N         uint64 `cbor:"n,omitempty"`
}

// AuditRow is one line of the box's audit log.
type AuditRow struct {
	Seq       uint64 `cbor:"seq" json:"seq"`
	AtMs      int64  `cbor:"atMs" json:"atMs"`
	Actor     string `cbor:"actor" json:"actor"`
	Role      string `cbor:"role" json:"role"`
	Principal string `cbor:"principal" json:"principal"`
	By        string `cbor:"by" json:"by"`
	What      string `cbor:"what" json:"what"`
	Target    string `cbor:"target" json:"target"`
	Result    string `cbor:"result" json:"result"`
}

// AuditPage is agent.ok's body for audit.get.
type AuditPage struct {
	Rows []AuditRow `cbor:"rows" json:"rows"`
	More bool       `cbor:"more" json:"more"`
}

// LogsGet asks for a redacted log excerpt.
type LogsGet struct {
	N     uint64 `cbor:"n,omitempty"`
	Level string `cbor:"level,omitempty"`
}

// LogLine is one redacted line. Src is an opaque source id for a driver's
// line, empty for the box's own.
type LogLine struct {
	Ts    int64  `cbor:"ts"`
	Level string `cbor:"level"`
	Src   string `cbor:"src,omitempty"`
	Msg   string `cbor:"msg"`
}

// Logs is the answer to logs.get. Dropped counts lines left out to stay
// within the limits.
type Logs struct {
	Lines   []LogLine `cbor:"lines"`
	Dropped uint64    `cbor:"dropped"`
}

// --------------------------------------------------------------------------
// The approval signature
// --------------------------------------------------------------------------

// ApprovalTag starts every signed array, so a signature for anything else is
// never one of these.
const ApprovalTag = "ftw.approval.v1"

// ApprovalSigBytes is r ‖ s, 32 bytes each.
const ApprovalSigBytes = 64

// ApprovalPubBytes is a SEC1 uncompressed P-256 point.
const ApprovalPubBytes = 65

// Errors from checking a signed body's shape. A signature that does not
// verify is ErrApprovalSignature.
var (
	ErrApprovalUnsorted  = errors.New("protocol: a signed list must be sorted with no repeats")
	ErrApprovalPub       = errors.New("protocol: approval key is not an uncompressed P-256 point")
	ErrApprovalSignature = errors.New("protocol: approval signature does not verify")
)

// SignedFor names the box and the phone an approval belongs to.
type SignedFor struct {
	// BoxKey is the box's X25519 static key, as in the pairing QR code.
	BoxKey []byte
	// DeviceID is the signing phone's device id.
	DeviceID string
}

// ApprovalBytes is the deterministic CBOR array a phone signs: the tag, what
// it approves, the box, the phone, its clock, then the fields.
func ApprovalBytes(what string, f SignedFor, atMs int64, fields ...any) ([]byte, error) {
	for _, v := range fields {
		if list, ok := v.([]string); ok && !sortedUnique(list) {
			return nil, ErrApprovalUnsorted
		}
	}
	arr := append([]any{ApprovalTag, what, f.BoxKey, f.DeviceID, atMs}, fields...)
	b, err := encMode.Marshal(arr)
	if err != nil {
		return nil, fmt.Errorf("protocol: encode approval: %w", err)
	}
	return b, nil
}

func capsArray(c Caps) []uint64 { return []uint64{c.HoldS, c.MaxW, c.PerMin} }

func emptyIfNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

// KeySetBytes is what approval.key.set signs: the key itself, as proof the
// phone holds its private half.
func KeySetBytes(f SignedFor, m ApprovalKeySet) ([]byte, error) {
	return ApprovalBytes(MsgApprovalKeySet, f, m.AtMs, m.Pub)
}

// AddBytes is what agent.add signs.
func AddBytes(f SignedFor, m AgentAdd) ([]byte, error) {
	return ApprovalBytes(MsgAgentAdd, f, m.AtMs, m.Key, m.Role, emptyIfNil(m.Scopes), emptyIfNil(m.Ops),
		capsArray(m.Caps), m.ExpiresAtMs, m.Principal.Client, m.Principal.ClientID, m.Principal.Ticket)
}

// WidenBytes is what agent.widen signs.
func WidenBytes(f SignedFor, m AgentWiden) ([]byte, error) {
	return ApprovalBytes(MsgAgentWiden, f, m.AtMs, m.ID, m.Epoch, emptyIfNil(m.Scopes), emptyIfNil(m.Ops),
		capsArray(m.Caps), m.ExpiresAtMs)
}

// ConfirmBytes is what agent.confirm signs to approve.
func ConfirmBytes(f SignedFor, m AgentConfirm) ([]byte, error) {
	return ApprovalBytes(MsgAgentConfirm, f, m.AtMs, m.ReqID)
}

// ParseApprovalKey reads a SEC1 uncompressed P-256 public key.
func ParseApprovalKey(pub []byte) (*ecdsa.PublicKey, error) {
	if len(pub) != ApprovalPubBytes || pub[0] != 4 {
		return nil, ErrApprovalPub
	}
	x := new(big.Int).SetBytes(pub[1:33])
	y := new(big.Int).SetBytes(pub[33:])
	curve := elliptic.P256()
	if !curve.IsOnCurve(x, y) {
		return nil, ErrApprovalPub
	}
	return &ecdsa.PublicKey{Curve: curve, X: x, Y: y}, nil
}

// VerifyApproval checks sig, r ‖ s, over SHA-256 of msg.
func VerifyApproval(pub []byte, msg, sig []byte) error {
	key, err := ParseApprovalKey(pub)
	if err != nil {
		return err
	}
	if len(sig) != ApprovalSigBytes {
		return ErrApprovalSignature
	}
	digest := sha256.Sum256(msg)
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(key, digest[:], r, s) {
		return ErrApprovalSignature
	}
	return nil
}

func sortedUnique(list []string) bool {
	return slices.IsSorted(list) && len(slices.Compact(slices.Clone(list))) == len(list)
}

// Refusal is a refusal on its way to the wire: a registry code and its
// machine-readable args. The box's layers return it so the one that answers
// the caller need not translate.
type Refusal struct {
	Code string
	Args map[string]any
}

func (r *Refusal) Error() string { return fmt.Sprintf("protocol: refused %s %v", r.Code, r.Args) }

// Refuse builds a Refusal.
func Refuse(code string, args map[string]any) *Refusal { return &Refusal{Code: code, Args: args} }
