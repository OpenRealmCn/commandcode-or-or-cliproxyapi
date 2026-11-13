package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestNormalizeProxyURL(t *testing.T) {
	cases := []struct{ in, def, want string }{
		{"HTTP://User:p%40ss@EXAMPLE.COM:080/", "", "http://User:p%40ss@example.com:80"},
		{"http://us%65r:p%3ass@EXAMPLE.COM:8080", "", "http://user:p%3Ass@example.com:8080"},
		{"https://example.com:443", "", "https://example.com:443"},
		{"socks5://[2001:DB8::1]:1080", "", "socks5://[2001:db8::1]:1080"},
		{"socks5h://user:p%3A%2F%25@[2001:db8::2]:1080", "", "socks5h://user:p%3A%2F%25@[2001:db8::2]:1080"},
		{"proxy.example:8080", "http", "http://proxy.example:8080"},
	}
	for _, tc := range cases {
		got, _, err := normalizeProxyURL(tc.in, tc.def)
		if err != nil || got != tc.want {
			t.Errorf("normalize(%q)=%q,%v want %q", tc.in, got, err, tc.want)
		}
	}
	for _, raw := range []string{"direct", "example.com:80", "http://x:0", "http://x:65536", "http://x:abc", "http://x/path:3", "http://x:80/path", "http://x:80?secret", "http://x:80#fragment", "http://2001:db8::1:80", "http://x:80\n", "ftp://x:21"} {
		if got, _, err := normalizeProxyURL(raw, ""); err == nil {
			t.Errorf("normalize(%q)=%q; want rejection", raw, got)
		}
	}
	if _, _, err := normalizeProxyURL("x:80", ""); err == nil {
		t.Fatal("missing default protocol accepted")
	}
}

func TestValidateProxyPoolDuplicatesAndLimits(t *testing.T) {
	resp, err := validateProxyPool(proxyPoolRequest{
		Lines:    "http://EXAMPLE.com:80\n\nhttp://example.com:080\nsocks5h://proxy:1080",
		Existing: []proxyPoolEntry{{ID: "px_old", URL: "https://example.com:443"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Valid != 2 || resp.Duplicates != 1 || resp.Errors != 0 || resp.Rows[1].Line != 3 || resp.Rows[2].Status != "valid" {
		t.Fatalf("response = %#v", resp)
	}
	if _, err := validateProxyPool(proxyPoolRequest{Lines: strings.Repeat("http://x:80\n", proxyPoolLineLimit+1)}); err == nil {
		t.Fatal("accepted more than 1000 lines")
	}
	if _, err := validateProxyPool(proxyPoolRequest{Existing: []proxyPoolEntry{{ID: "bad", URL: "not-a-proxy"}}}); err == nil {
		t.Fatal("accepted malformed existing directory")
	}
	if _, err := validateProxyPool(proxyPoolRequest{Existing: make([]proxyPoolEntry, proxyPoolLineLimit+1)}); err == nil {
		t.Fatal("accepted oversized existing directory")
	}
}

func TestProxyValidationManagementRouteAndLimits(t *testing.T) {
	m := NewManager(nil)
	resp, err := m.HandleManagement(context.Background(), pluginapi.ManagementRequest{Method: http.MethodPost, Path: "/v0/management/plugins/" + pluginName + "/proxy-pool/validate", Body: []byte(`{"lines":"https://example.com:443"}`)})
	if err != nil || (resp.StatusCode != 0 && resp.StatusCode != http.StatusOK) {
		t.Fatalf("validate = %d %s %v", resp.StatusCode, resp.Body, err)
	}
	var got proxyValidationResponse
	if json.Unmarshal(resp.Body, &got) != nil || got.Valid != 1 {
		t.Fatalf("validate response = %s", resp.Body)
	}
	resp, _ = m.HandleManagement(context.Background(), pluginapi.ManagementRequest{Method: http.MethodPost, Path: "/v0/management/plugins/" + pluginName + "/proxy-pool/validate", Body: make([]byte, proxyPoolBodyLimit+1)})
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status = %d", resp.StatusCode)
	}

	var registration struct {
		Routes []struct {
			Path string `json:"path"`
		} `json:"routes"`
	}
	decodeResult(t, mustHandle(t, m, pluginabi.MethodManagementRegister, []byte(`{}`)), &registration)
	found := false
	for _, r := range registration.Routes {
		if r.Path == "/plugins/"+pluginName+"/proxy-pool/validate" {
			found = true
		}
	}
	if !found {
		t.Fatal("proxy validation route not registered")
	}
}

func TestProxySummaryRedactsSecrets(t *testing.T) {
	const apiKey = "sk-api-key-secret"
	const password = "proxy-password-secret"
	card := quotaCard{}
	populateAccountDetails(&card, []byte(`{"api_key":"`+apiKey+`","commandcode_proxy_id":"px_a","proxy_url":"socks5h://user:`+password+`@Proxy.Example:1080"}`))
	wire, _ := json.Marshal(card)
	if card.ProxyID != "px_a" || !card.ProxyConfigured || card.ProxyURLHash == "" {
		t.Fatalf("proxy summary = %#v", card)
	}
	if strings.Contains(string(wire), apiKey) || strings.Contains(string(wire), password) || strings.Contains(card.ProxyDisplay, "user") {
		t.Fatalf("proxy summary leaked secret: %s", wire)
	}
	bad := quotaCard{}
	populateAccountDetails(&bad, []byte("{"))
	if !bad.ProxyReadError {
		t.Fatal("invalid auth JSON did not set proxy_read_error")
	}
}

func TestProxyValidationHashesAndExistingDuplicate(t *testing.T) {
	resp, err := validateProxyPool(proxyPoolRequest{
		Lines:    "\nHTTP://user:p%40ss@EXAMPLE.COM:0080/\r\nhttp://user:p%40ss@example.com:80\r\nhttps://existing.example:443",
		Existing: []proxyPoolEntry{{ID: "px_existing", URL: "HTTPS://EXISTING.example:0443/"}},
	})
	if err != nil || resp.Valid != 1 || resp.Duplicates != 2 || len(resp.Rows) != 3 {
		t.Fatalf("validation=%#v err=%v", resp, err)
	}
	canonical := "http://user:p%40ss@example.com:80"
	if resp.Rows[0].URL != canonical || resp.Rows[0].Line != 2 || resp.Rows[1].Line != 3 || resp.Rows[0].URLHash != proxyURLHash(canonical) || resp.Rows[1].URLHash != resp.Rows[0].URLHash || resp.Rows[1].URL != "" || resp.Rows[2].DuplicateID != "px_existing" || len(resp.Rows[2].URLHash) != 64 {
		t.Fatalf("hash/duplicate contract=%#v", resp.Rows)
	}
	// These values form the identity bridge between validation and quota cards.
	var card quotaCard
	populateAccountDetails(&card, []byte(`{"proxy_url":"HTTP://user:p%40ss@EXAMPLE.COM:0080/"}`))
	if card.ProxyURLHash != resp.Rows[0].URLHash {
		t.Fatal("quota/validation hash mismatch")
	}
}

func TestProxyValidationSafetyAndRequestBoundaries(t *testing.T) {
	for _, raw := range []string{
		"http://user:password-secret@host:80?token=secret", "http://user:password-secret@host:bad", "http://user:password-secret@host:80/path", "http://user:p%0ass@host:80", "http://host:80\t", "http://host:80#", "http://host:80?", "http://[example]:80", "http://:80", "http://host", "http://host:80/%2f",
	} {
		canonical, display, err := normalizeProxyURL(raw, "")
		if err == nil || canonical != "" || display != "" || strings.Contains(err.Error(), "password-secret") {
			t.Errorf("unsafe acceptance/error for %q", raw)
		}
	}
	accepted, err := validateProxyPool(proxyPoolRequest{Lines: strings.Repeat("http://x:80\n", proxyPoolLineLimit)})
	if err != nil || len(accepted.Rows) != proxyPoolLineLimit {
		t.Fatalf("1000-line boundary failed: %v", err)
	}
	for _, raw := range []string{`null`, `[]`, `{`, `{"existing":[{"id":"px_bad","url":"http://user:password-secret@host:bad"}]}`, `{"lines":1}`} {
		result, err := handleProxyValidation([]byte(raw))
		if err != nil || result.StatusCode != http.StatusBadRequest || strings.Contains(string(result.Body), "password-secret") {
			t.Errorf("bad request status=%d error=%v body=%s", result.StatusCode, err, result.Body)
		}
	}
	for _, raw := range []string{`{}`, `{"lines":"\n\r\n","existing":[]}`} {
		result, err := handleProxyValidation([]byte(raw))
		var wire struct {
			Rows []proxyValidationRow `json:"rows"`
		}
		if err != nil || json.Unmarshal(result.Body, &wire) != nil || wire.Rows == nil || len(wire.Rows) != 0 {
			t.Errorf("empty result=%s err=%v", result.Body, err)
		}
	}
}

func TestProxySummaryUnboundExternalAndMalformed(t *testing.T) {
	for _, test := range []struct {
		raw                   string
		configured, readError bool
		id                    string
	}{
		{`{}`, false, false, ""},
		{`{"commandcode_proxy_id":"px_orphan"}`, false, false, "px_orphan"},
		{`{"proxy_url":"https://host:443"}`, true, false, ""},
		{`{"proxy_url":"http://user:password-secret@host:bad"}`, true, true, ""},
		{`{"proxy_url":42}`, false, true, ""},
		{`null`, false, true, ""},
	} {
		var card quotaCard
		populateAccountDetails(&card, []byte(test.raw))
		wire, _ := json.Marshal(card)
		if card.ProxyConfigured != test.configured || card.ProxyReadError != test.readError || card.ProxyID != test.id || strings.Contains(string(wire), "password-secret") {
			t.Errorf("summary=%s input=%s", wire, test.raw)
		}
	}
}
