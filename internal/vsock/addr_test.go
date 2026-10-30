package vsock

import "testing"

func TestCIDList(t *testing.T) {
	var l CIDList
	if !l.Allowed(7) {
		t.Fatal("empty list must allow every CID")
	}
	if err := l.Set("2, 3,16"); err != nil {
		t.Fatal(err)
	}
	if err := l.Set("3,42"); err != nil {
		t.Fatal(err)
	}
	if got, want := l.String(), "2,3,16,42"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	for cid, want := range map[uint32]bool{2: true, 3: true, 16: true, 42: true, 4: false} {
		if l.Allowed(cid) != want {
			t.Errorf("Allowed(%d) = %v, want %v", cid, !want, want)
		}
	}
	if err := l.Set("host"); err == nil {
		t.Error("expected error for non-numeric CID")
	}
}

func TestAddrString(t *testing.T) {
	if s := (Addr{CID: CIDAny, Port: 5000}).String(); s != "any:5000" {
		t.Errorf("got %q", s)
	}
	if s := (Addr{CID: 2, Port: 1035}).String(); s != "2:1035" {
		t.Errorf("got %q", s)
	}
}
