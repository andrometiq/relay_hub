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
