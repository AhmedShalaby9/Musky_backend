package server

import (
	"testing"

	"musky/backend/internal/model"
)

func TestCanManageUserContacts(t *testing.T) {
	owner := model.User{ID: 5, Role: model.Trader}
	coworker := model.User{ID: 6, Role: model.Admin}
	superAdmin := model.User{ID: 1, Role: model.SuperAdmin}

	if !canManageUserContacts(owner, owner.ID) {
		t.Fatal("the account owner must be able to manage their own contacts")
	}
	if !canManageUserContacts(superAdmin, owner.ID) {
		t.Fatal("super admin must be able to manage any user's contacts")
	}
	if canManageUserContacts(coworker, owner.ID) {
		t.Fatal("a different tenant member must not manage someone else's contacts")
	}
}
