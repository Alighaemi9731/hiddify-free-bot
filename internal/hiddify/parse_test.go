package hiddify

import "testing"

func TestParseAdminLink(t *testing.T) {
	ap, err := ParseAdminLink("https://mainsub.iranfilm-dl.online/knrvQP52yPDxcsCaK9dtX2SH55BPL/b701bbe5-f425-44f3-a287-5b70161d2142/admin/adminuser/")
	if err != nil {
		t.Fatal(err)
	}
	if ap.Domain != "mainsub.iranfilm-dl.online" {
		t.Errorf("domain=%q", ap.Domain)
	}
	if ap.ProxyPath != "knrvQP52yPDxcsCaK9dtX2SH55BPL" {
		t.Errorf("proxy=%q", ap.ProxyPath)
	}
	if ap.AdminUUID != "b701bbe5-f425-44f3-a287-5b70161d2142" {
		t.Errorf("uuid=%q", ap.AdminUUID)
	}
}

func TestParseSubLink(t *testing.T) {
	sp, err := ParseSubLink("https://mainsub.iranfilm-dl.online/NH3tGehZZF/fe8c7bc1-ad6e-4fc7-b4e2-4f450a25fbb8/#Amo-mojtaba")
	if err != nil {
		t.Fatal(err)
	}
	if sp.Domain != "mainsub.iranfilm-dl.online" {
		t.Errorf("domain=%q", sp.Domain)
	}
	if sp.ProxyPath != "NH3tGehZZF" {
		t.Errorf("proxy=%q", sp.ProxyPath)
	}
}

func TestSubLink(t *testing.T) {
	got := SubLink("d.com", "p", "uuid-1", "clash", "name x")
	want := "https://d.com/p/uuid-1/clash/#name%20x"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
	got = SubLink("d.com", "p", "uuid-1", "auto", "")
	if got != "https://d.com/p/uuid-1/" {
		t.Errorf("auto: got %q", got)
	}
}
