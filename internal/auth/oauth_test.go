package auth

import "testing"

func TestParsePasted(t *testing.T) {
	code, state, err := ParsePasted("  sc1.code_AbC-d_e.st4te-_x \n")
	if err != nil || code != "code_AbC-d_e" || state != "st4te-_x" {
		t.Fatalf("%q %q %v", code, state, err)
	}
	code, state, err = ParsePasted("https://supercool.com/oauth/cli-code?code=code_1&state=s2")
	if err != nil || code != "code_1" || state != "s2" {
		t.Fatalf("%q %q %v", code, state, err)
	}
	if _, _, err := ParsePasted("https://x/cb?error=access_denied"); err == nil {
		t.Fatal("a cancelled sign-in parsed")
	}
	for _, bad := range []string{"code_1", "sc1.", "sc1.onlycode"} {
		if _, _, err := ParsePasted(bad); err == nil {
			t.Fatalf("%q parsed", bad)
		}
	}
}

func TestChallengeIsS256(t *testing.T) {
	// RFC 7636 appendix B.
	if got := challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatal(got)
	}
}
