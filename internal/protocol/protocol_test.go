package protocol

import "testing"

func TestVerdict(t *testing.T) {
	ac := func(s ...string) Report {
		var r Report
		for _, x := range s {
			r.ACs = append(r.ACs, ACResult{Status: x})
		}
		return r
	}
	cases := []struct {
		r    Report
		want string
	}{
		{ac(), VerdictPartial},
		{ac(ACPass, ACPass), VerdictPass},
		{ac(ACPass, ACUnverifiable), VerdictPartial},
		{ac(ACUnverifiable, ACFail, ACPass), VerdictFail},
		{ac(ACPass, "weird"), VerdictPartial},
	}
	for i, c := range cases {
		if got := c.r.Verdict(); got != c.want {
			t.Errorf("case %d: got %s want %s", i, got, c.want)
		}
	}
}
