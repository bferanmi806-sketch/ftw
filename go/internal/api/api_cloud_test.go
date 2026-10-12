package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/srcfl/ftw/go/internal/apiauth"
	"github.com/srcfl/ftw/go/internal/cloudlink"
)

type stubCloud struct {
	granted []cloudlink.GrantRequest
	revoked []string
	err     error
}

func (c *stubCloud) Status() cloudlink.Status   { return cloudlink.Status{State: "off"} }
func (c *stubCloud) Preview() cloudlink.Preview { return cloudlink.Preview{State: "off", Sample: true} }
func (c *stubCloud) Grant(req cloudlink.GrantRequest) error {
	c.granted = append(c.granted, req)
	return c.err
}
func (c *stubCloud) Revoke(by string) error {
	c.revoked = append(c.revoked, by)
	return nil
}

func viewerRequest(method, target, body string) *http.Request {
	r := sessionRequest(method, target, body)
	caller, _ := apiauth.FromRequest(r)
	caller.Role = apiauth.RoleViewer
	caller.Scopes = apiauth.NewScopeSet(apiauth.RoleScopes[apiauth.RoleViewer]...)
	return r.WithContext(apiauth.WithCaller(r.Context(), caller))
}

func loopbackRequest(method, target, body string) *http.Request {
	r := localRequest(method, target, body)
	r.Host = "localhost:8080"
	r.RemoteAddr = "127.0.0.1:5555"
	return r
}

const grantBody = `{"scopes":["ftw.live.read"],"policy":"2026-10"}`

// Turning FTW Cloud on is an owner's act: over an app session, or at the box.
// A LAN client without the house password is refused, unlike the rest of the
// LAN API, because this sends the house out of it.
func TestWhoMayTurnFTWCloudOn(t *testing.T) {
	cases := []struct {
		name string
		req  *http.Request
		want int
	}{
		{"owner over a session", sessionRequest(http.MethodPost, "/api/cloud/grant", grantBody), http.StatusOK},
		{"viewer over a session", viewerRequest(http.MethodPost, "/api/cloud/grant", grantBody), http.StatusForbidden},
		{"the box itself", loopbackRequest(http.MethodPost, "/api/cloud/grant", grantBody), http.StatusOK},
		{"a LAN client", localRequest(http.MethodPost, "/api/cloud/grant", grantBody), http.StatusForbidden},
		{"a LAN client with the house password", func() *http.Request {
			r := localRequest(http.MethodPost, "/api/cloud/grant", grantBody)
			return r.WithContext(withLANSecret(r.Context(), true))
		}(), http.StatusOK},
		{"through a proxy", func() *http.Request {
			r := loopbackRequest(http.MethodPost, "/api/cloud/grant", grantBody)
			r.Header.Set("X-Forwarded-For", "203.0.113.9")
			return r
		}(), http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cloud := &stubCloud{}
			s := New(&Deps{Cloud: cloud})
			w := httptest.NewRecorder()
			s.handleCloudGrant(w, c.req)
			if w.Code != c.want {
				t.Fatalf("got %d, want %d: %s", w.Code, c.want, w.Body.String())
			}
			if granted := len(cloud.granted) == 1; granted != (c.want == http.StatusOK) {
				t.Fatalf("granted = %v with status %d", granted, w.Code)
			}
		})
	}
}

// The audit line names the door, never anything the caller sent.
func TestTheGrantRecordsWhichDoorAsked(t *testing.T) {
	cloud := &stubCloud{}
	s := New(&Deps{Cloud: cloud})
	s.handleCloudGrant(httptest.NewRecorder(), sessionRequest(http.MethodPost, "/api/cloud/grant",
		`{"scopes":["ftw.live.read"],"policy":"p","By":"someone else"}`))
	s.handleCloudGrant(httptest.NewRecorder(), loopbackRequest(http.MethodPost, "/api/cloud/grant", grantBody))
	if len(cloud.granted) != 2 || cloud.granted[0].By != "app:aaaa1111" || cloud.granted[1].By != "box" {
		t.Fatalf("granted %+v", cloud.granted)
	}
}

// Turning it off narrows access, so the LAN may, without the password.
func TestAnyDoorMayTurnFTWCloudOff(t *testing.T) {
	for name, req := range map[string]*http.Request{
		"owner over a session": sessionRequest(http.MethodDelete, "/api/cloud/grant", ""),
		"a LAN client":         localRequest(http.MethodDelete, "/api/cloud/grant", ""),
	} {
		cloud := &stubCloud{}
		s := New(&Deps{Cloud: cloud})
		w := httptest.NewRecorder()
		s.handleCloudRevoke(w, req)
		if w.Code != http.StatusOK || len(cloud.revoked) != 1 {
			t.Fatalf("%s: %d, revoked %v", name, w.Code, cloud.revoked)
		}
	}
}

// Only an owner reads what leaves the house; a viewer sees no grant screen.
func TestOnlyAnOwnerReadsTheCloudStatusAndPreview(t *testing.T) {
	s := New(&Deps{Cloud: &stubCloud{}})
	for _, target := range []string{"/api/cloud", "/api/cloud/preview"} {
		handler := s.handleCloudStatus
		if target == "/api/cloud/preview" {
			handler = s.handleCloudPreview
		}
		w := httptest.NewRecorder()
		handler(w, sessionRequest(http.MethodGet, target, ""))
		if w.Code != http.StatusOK {
			t.Fatalf("owner %s: %d", target, w.Code)
		}
		w = httptest.NewRecorder()
		handler(w, viewerRequest(http.MethodGet, target, ""))
		if w.Code != http.StatusForbidden {
			t.Fatalf("viewer %s: %d", target, w.Code)
		}
	}
}

func TestCloudRoutesSayWhenThereIsNoCloud(t *testing.T) {
	s := New(&Deps{})
	w := httptest.NewRecorder()
	s.handleCloudStatus(w, sessionRequest(http.MethodGet, "/api/cloud", ""))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d", w.Code)
	}
}

func TestCloudGrantRefusalsKeepTheirMeaning(t *testing.T) {
	for err, want := range map[error]int{
		cloudlink.ErrGrantRequest: http.StatusBadRequest,
		cloudlink.ErrPurgePending: http.StatusConflict,
		cloudlink.ErrNoService:    http.StatusServiceUnavailable,
	} {
		s := New(&Deps{Cloud: &stubCloud{err: err}})
		w := httptest.NewRecorder()
		s.handleCloudGrant(w, sessionRequest(http.MethodPost, "/api/cloud/grant", grantBody))
		if w.Code != want {
			t.Errorf("%v: got %d, want %d", err, w.Code, want)
		}
	}
}

// Through the router, so the tiers are the ones the passthrough reads.
func TestCloudRoutesAreRegisteredWithTheirTiers(t *testing.T) {
	s := New(&Deps{Cloud: &stubCloud{}})
	for _, c := range []struct {
		method, path string
		tier         apiauth.Tier
		noStepUp     bool
	}{
		{http.MethodGet, "/api/cloud", apiauth.TierRead, false},
		{http.MethodGet, "/api/cloud/preview", apiauth.TierRead, false},
		{http.MethodPost, "/api/cloud/grant", apiauth.TierConfigure, true},
		{http.MethodDelete, "/api/cloud/grant", apiauth.TierConfigure, true},
	} {
		facts := s.Route(httptest.NewRequest(c.method, c.path, nil))
		if facts.Tier != c.tier || facts.NoStepUp != c.noStepUp {
			t.Errorf("%s %s: %+v", c.method, c.path, facts)
		}
	}
}
