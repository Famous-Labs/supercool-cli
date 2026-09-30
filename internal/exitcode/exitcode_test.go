package exitcode

import "testing"

func TestWorst(t *testing.T) {
	if Worst() != OK || Worst(OK, OK) != OK {
		t.Fatal("empty or all-ok should be OK")
	}
	if Worst(OK, DownloadFailed, Expired) != Expired {
		t.Fatal("expired outranks a download failure")
	}
	if Worst(Stopped, Failed, Credits) != Failed {
		t.Fatal("failed outranks stopped and credits")
	}
	if Worst(Failed, LoginNeeded) != LoginNeeded {
		t.Fatal("login needed is the most severe")
	}
}

func TestForOutcome(t *testing.T) {
	want := map[string]int{"completed": OK, "stalled_credits": Credits, "failed": Failed, "stopped": Stopped,
		"expired": Expired, "unknown": Unknown, "busy": RateLimited, "weird": Error}
	for o, c := range want {
		if ForOutcome(o) != c {
			t.Errorf("%s -> %d, want %d", o, ForOutcome(o), c)
		}
	}
}
