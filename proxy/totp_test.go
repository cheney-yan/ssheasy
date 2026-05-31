package main

import (
	"testing"
	"time"
)

// RFC 6238 Appendix B reference vectors (SHA1, secret = ASCII "12345678901234567890").
func TestTOTPVectors(t *testing.T) {
	totpSeed = []byte("12345678901234567890")
	expect := map[int64]string{
		59 / 30:          "287082",
		1111111109 / 30:  "081804",
		1234567890 / 30:  "005924",
		2000000000 / 30:  "279037",
		20000000000 / 30: "353130",
	}
	for counter, want := range expect {
		if got := totpAt(counter); got != want {
			t.Errorf("counter %d: got %s, want %s", counter, got, want)
		}
	}
}

func TestVerifyTOTPWindow(t *testing.T) {
	totpSeed = []byte("12345678901234567890")
	// T = 59 -> counter 1; codes for counters 0,1,2 should all verify.
	for _, c := range []int64{0, 1, 2} {
		code := totpAt(c)
		if !verifyTOTP(code, time.Unix(1*30, 0)) {
			t.Errorf("code for counter %d (%s) did not verify within window", c, code)
		}
	}
	if verifyTOTP("000000", time.Unix(1*30, 0)) && totpAt(1) != "000000" {
		t.Error("clearly-wrong code unexpectedly verified")
	}
}
