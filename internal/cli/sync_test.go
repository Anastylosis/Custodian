package cli

import "testing"

func TestDefaultLedgerPath(t *testing.T) {
	cases := map[string]string{
		"/data/custodian.sqlite": "/data/custodian.ledger.sqlite",
		"custodian.sqlite":       "custodian.ledger.sqlite",
		"nodotfile":              "nodotfile.ledger.sqlite",
		"/a/b.c/custodian.db":    "/a/b.c/custodian.ledger.sqlite",
	}
	for in, want := range cases {
		if got := defaultLedgerPath(in); got != want {
			t.Errorf("defaultLedgerPath(%q) = %q, want %q", in, got, want)
		}
	}
}
