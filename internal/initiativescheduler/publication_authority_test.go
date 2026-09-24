package initiativescheduler

import "testing"

func TestPublicationAuthorityCommitsExactlyOnce(t *testing.T) {
	authority, err := NewPublicationAuthority([16]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := authority.Prepare(100, 500, 1, 2, 3, ActionAskOne)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.Commit(200, lease, 1, 2, 3, ActionAskOne); err != nil {
		t.Fatal(err)
	}
	if err := authority.Commit(201, lease, 1, 2, 3, ActionAskOne); err == nil {
		t.Fatal("second commit succeeded")
	}
}

func TestPublicationAuthorityRejectsStaleOrRevokedLease(t *testing.T) {
	tests := []struct {
		name   string
		commit func(*PublicationAuthority, Lease) error
	}{
		{"expired", func(a *PublicationAuthority, l Lease) error { return a.Commit(601, l, 1, 2, 3, ActionAskOne) }},
		{"user", func(a *PublicationAuthority, l Lease) error { return a.Commit(200, l, 2, 2, 3, ActionAskOne) }},
		{"goal", func(a *PublicationAuthority, l Lease) error { return a.Commit(200, l, 1, 3, 3, ActionAskOne) }},
		{"turn", func(a *PublicationAuthority, l Lease) error { return a.Commit(200, l, 1, 2, 4, ActionAskOne) }},
		{"action", func(a *PublicationAuthority, l Lease) error { return a.Commit(200, l, 1, 2, 3, ActionOfferChoice) }},
		{"capability", func(a *PublicationAuthority, l Lease) error {
			l.SessionCapability[0]++
			return a.Commit(200, l, 1, 2, 3, ActionAskOne)
		}},
		{"abort", func(a *PublicationAuthority, l Lease) error {
			a.Abort()
			return a.Commit(200, l, 1, 2, 3, ActionAskOne)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authority, err := NewPublicationAuthority([16]byte{1})
			if err != nil {
				t.Fatal(err)
			}
			lease, err := authority.Prepare(100, 500, 1, 2, 3, ActionAskOne)
			if err != nil {
				t.Fatal(err)
			}
			if err := test.commit(authority, lease); err == nil {
				t.Fatal("unsafe commit succeeded")
			}
		})
	}
}

func TestPublicationAuthorityCannotReviveAfterAbort(t *testing.T) {
	authority, err := NewPublicationAuthority([16]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	authority.Abort()
	authority.Abort()
	if _, err := authority.Prepare(100, 500, 1, 2, 3, ActionAskOne); err == nil {
		t.Fatal("prepare revived an aborted authority")
	}
}
