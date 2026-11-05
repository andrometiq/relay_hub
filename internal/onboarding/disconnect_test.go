package onboarding

import (
	"testing"

	"github.com/andrometiq/relay_hub/internal/auth"
	"github.com/andrometiq/relay_hub/internal/containers"
)

func TestDisconnectRevokesTheSiteAndReEnrolmentReconnects(t *testing.T) {
	h := newHarness(t)
	h.signupContainer()
	site, token := h.enrolledSite()
	if done := h.startSignup(site, "new").finish("FINISH", "230", "120", "code-230"); done.State != "completed" {
		t.Fatalf("signup %s", done.State)
	}
	if _, err := h.auth.Authenticate(h.ctx, token); err != nil {
		t.Fatal("the enrolled token is not live")
	}
	operator := h.operatorToken(site)
	if err := h.service.Disconnect(h.ctx, site, false); failureCode(err) != "confirm_required" {
		t.Fatalf("disconnect without confirm: %v", err)
	}
	reloads := h.reloads
	if err := h.service.Disconnect(h.ctx, site, true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.auth.Authenticate(h.ctx, token); err != auth.ErrDenied {
		t.Fatalf("the token works after disconnect: %v", err)
	}
	var live int
	if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM relay_api_tokens t JOIN relay_users u ON u.id=t.user_id
 WHERE t.site_id=$1 AND t.revoked_at IS NULL AND u.kind='service'`, site).Scan(&live); err != nil || live != 0 {
		t.Fatalf("live service tokens after disconnect %d %v", live, err)
	}
	stored, err := h.store.GetSite(h.ctx, site)
	if err != nil || stored.Enabled || h.reloads != reloads+1 {
		t.Fatalf("disconnected site %+v %v", stored, err)
	}
	number, _ := h.store.GetChannel(h.ctx, containers.ChannelRef{Channel: "WhatsApp", Key: "wa-120"})
	if number.Site != site {
		t.Fatal("disconnect unlinked the site's number")
	}
	if err = h.service.Disconnect(h.ctx, site, true); failureCode(err) != "not_found" {
		t.Fatalf("second disconnect: %v", err)
	}

	again, fresh := h.enrolledSite()
	if again != site {
		t.Fatalf("re-enrolment made another site %q", again)
	}
	if principal, err := h.auth.Authenticate(h.ctx, fresh); err != nil || principal.Site != site {
		t.Fatalf("re-enrolled token %v %v", principal, err)
	}
	if principal, err := h.auth.Authenticate(h.ctx, operator); err != nil || principal.Site != site {
		t.Fatalf("disconnect revoked an operator token of another tool: %v", err)
	}
	if stored, _ = h.store.GetSite(h.ctx, site); !stored.Enabled {
		t.Fatal("re-enrolment left the site off")
	}
}

func TestReEnrolmentKeepsASiteTheAdminTurnedOff(t *testing.T) {
	h := newHarness(t)
	site, _ := h.enrolledSite()
	if _, err := h.pool.Exec(h.ctx, `UPDATE relay_sites SET enabled=false WHERE site_id=$1`, site); err != nil {
		t.Fatal(err)
	}
	h.enrolledSite()
	if stored, _ := h.store.GetSite(h.ctx, site); stored.Enabled {
		t.Fatal("re-enrolment turned on a site the admin turned off")
	}
}

// operatorToken issues a token for site the way an operator does for another tool.
func (h *harness) operatorToken(site string) string {
	h.t.Helper()
	if err := h.auth.Bootstrap(h.ctx, "operator", "operator-password-1234"); err != nil {
		h.t.Fatal(err)
	}
	session, err := h.auth.Login(h.ctx, "operator", "operator-password-1234", "test")
	if err != nil {
		h.t.Fatal(err)
	}
	admin, err := h.auth.Session(h.ctx, session)
	if err != nil {
		h.t.Fatal(err)
	}
	token, err := h.auth.Issue(h.ctx, admin, "Reporting tool", site, 0)
	if err != nil {
		h.t.Fatal(err)
	}
	return token
}
