package cli

import "testing"

func TestActiveCartIDPicksACTIVE(t *testing.T) {
	// The saved-for-later cart is listed first on purpose: picking carts[0]
	// would write the weekly order into the wrong cart.
	body := []byte(`{"data":{"carts":[
		{"id":"saved-1","type":"SAVED_FOR_LATER"},
		{"id":"active-1","type":"ACTIVE"}]}}`)
	id, err := activeCartID(body)
	if err != nil {
		t.Fatal(err)
	}
	if id != "active-1" {
		t.Fatalf("got %q, want %q", id, "active-1")
	}
}

func TestActiveCartIDErrorsWhenNoneActive(t *testing.T) {
	if _, err := activeCartID([]byte(`{"data":{"carts":[{"id":"s","type":"SAVED_FOR_LATER"}]}}`)); err == nil {
		t.Fatal("want error when no ACTIVE cart exists")
	}
	if _, err := activeCartID([]byte(`{"data":{"carts":[]}}`)); err == nil {
		t.Fatal("want error when the account has no carts")
	}
}
