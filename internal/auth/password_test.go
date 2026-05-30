package auth

import "testing"

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if hash == "" {
		t.Fatal("empty hash")
	}
	ok, err := VerifyPassword("correct horse battery staple", hash)
	if err != nil || !ok {
		t.Fatalf("verify good: ok=%v err=%v", ok, err)
	}
	ok, err = VerifyPassword("wrong password", hash)
	if err != nil {
		t.Fatalf("verify bad: err=%v", err)
	}
	if ok {
		t.Fatal("verify bad: should not match")
	}
}

func TestVerifyMalformedHash(t *testing.T) {
	if _, err := VerifyPassword("x", "garbage"); err == nil {
		t.Fatal("want error on malformed hash")
	}
}

func TestValidateUsername(t *testing.T) {
	cases := map[string]bool{
		"ab":              false, // too short
		"abc":             true,
		"declan":          true,
		"declan_blanc_99": true,
		"Declan":          false, // uppercase
		"declan!":         false,
		"":                false,
	}
	for in, want := range cases {
		got := ValidateUsername(in) == ""
		if got != want {
			t.Errorf("ValidateUsername(%q) ok=%v want=%v", in, got, want)
		}
	}
}
