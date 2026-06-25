package hiddify

import (
	"context"
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveFetchConfigs hits a real sub link when HIDDIFY_SUB_URL is set.
func TestLiveFetchConfigs(t *testing.T) {
	url := os.Getenv("HIDDIFY_SUB_URL")
	if url == "" {
		t.Skip("set HIDDIFY_SUB_URL to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfgs, err := FetchConfigs(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("fetched %d configs", len(cfgs))
	for i, c := range cfgs {
		if !schemeRe.MatchString(c) {
			t.Errorf("config %d not a URI: %.60q", i, c)
		}
		t.Logf("  %d) %.80s", i+1, c)
	}
}

func TestParseSubBody(t *testing.T) {
	// Real Hiddify shape: a fake info line + a real config, base64-encoded.
	plain := "trojan://1@16.02--2026.06.25.time:1602?sni=fake_ip_for_sub_link&security=tls#info\n" +
		"vless://bf96649e-1de1-48f3-b31d-9f68b0f1535c@shaparak.ir.example.boats:443?security=reality#srv1\n" +
		"vmess://eyJhZGQiOiJ4In0=\n"
	b64 := base64.StdEncoding.EncodeToString([]byte(plain))

	got := ParseSubBody([]byte(b64))
	if len(got) != 2 {
		t.Fatalf("expected 2 real configs, got %d: %v", len(got), got)
	}
	if !strings.HasPrefix(got[0], "vless://") || !strings.HasPrefix(got[1], "vmess://") {
		t.Errorf("unexpected configs: %v", got)
	}
	for _, c := range got {
		if strings.Contains(c, "fake_ip_for_sub_link") {
			t.Errorf("fake info line leaked: %s", c)
		}
	}
}

func TestParseSubBodyPlain(t *testing.T) {
	// Some panels return plain text (not base64).
	plain := "vless://abc@h:443#x\nss://zzz@h:443#y\n"
	got := ParseSubBody([]byte(plain))
	if len(got) != 2 {
		t.Fatalf("plain: expected 2, got %d: %v", len(got), got)
	}
}
