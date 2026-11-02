package auth

import (
	"context"
	"errors"
	"testing"
)

func TestServiceUserHoldsOneLiveSiteToken(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.DB.Exec(ctx, "INSERT INTO relay_sites(site_id,origin) VALUES('shop','https://shop.invalid')"); err != nil {
		t.Fatal(err)
	}
	issue := func() (int64, string) {
		tx, err := s.DB.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		user, err := ServiceUser(ctx, tx, "shop")
		if err != nil {
			t.Fatal(err)
		}
		token, err := RotateServiceToken(ctx, tx, user, "shop")
		if err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return user, token
	}
	firstUser, first := issue()
	secondUser, second := issue()
	if firstUser != secondUser {
		t.Fatal("re-enrolment created a second service user")
	}
	if _, err := s.Authenticate(ctx, first); !errors.Is(err, ErrDenied) {
		t.Fatal("rotated token still works")
	}
	if p, err := s.Authenticate(ctx, second); err != nil || p.Site != "shop" || p.UserID != secondUser {
		t.Fatalf("service token %+v %v", p, err)
	}
	if _, err := s.Login(ctx, "site:shop", "", "test"); !errors.Is(err, ErrDenied) {
		t.Fatal("service user logged in")
	}
	if err := s.Bootstrap(ctx, "first-admin", "a-long-admin-password"); err != nil {
		t.Fatal("service user blocked bootstrap", err)
	}
}
