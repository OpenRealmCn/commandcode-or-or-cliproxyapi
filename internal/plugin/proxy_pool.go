package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	proxyPoolBodyLimit = 1 << 20
	proxyPoolLineLimit = 1000
)

type proxyPoolRequest struct {
	Lines           string           `json:"lines"`
	DefaultProtocol string           `json:"default_protocol"`
	Existing        []proxyPoolEntry `json:"existing"`
}

type proxyPoolEntry struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	URL   string `json:"url"`
}

type proxyValidationRow struct {
	Line        int    `json:"line"`
	Status      string `json:"status"`
	URL         string `json:"url,omitempty"`
	URLHash     string `json:"url_hash,omitempty"`
	Display     string `json:"display,omitempty"`
	Error       string `json:"error,omitempty"`
	DuplicateID string `json:"duplicate_id,omitempty"`
}

type proxyValidationResponse struct {
	Rows       []proxyValidationRow `json:"rows"`
	Valid      int                  `json:"valid"`
	Duplicates int                  `json:"duplicates"`
	Errors     int                  `json:"errors"`
}

func proxySchemeSupported(scheme string) bool {
	switch scheme {
	case "http", "https", "socks5", "socks5h":
		return true
	}
	return false
}

func proxyHasWhitespace(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) })
}

// normalizeProxyURL is deliberately pure parsing. Directory validation must not
// dial a proxy, construct a transport, or suggest that parsing proves egress.
// Never forward net/url errors: they can embed the complete input and password.
func normalizeProxyURL(raw, defaultProtocol string) (string, string, error) {
	if raw == "" || strings.EqualFold(raw, "direct") || strings.EqualFold(raw, "none") {
		return "", "", fmt.Errorf("代理地址不能为空或直连")
	}
	if proxyHasWhitespace(raw) {
		return "", "", fmt.Errorf("代理地址不能包含空白或控制字符")
	}
	if !strings.Contains(raw, "://") {
		if !proxySchemeSupported(defaultProtocol) {
			return "", "", fmt.Errorf("代理地址必须包含协议")
		}
		raw = defaultProtocol + "://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return "", "", fmt.Errorf("代理地址格式无效")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if !proxySchemeSupported(u.Scheme) {
		return "", "", fmt.Errorf("不支持的代理协议")
	}
	if u.Opaque != "" || u.Host == "" || u.Hostname() == "" || strings.ContainsAny(raw, "?#") || (u.EscapedPath() != "" && u.EscapedPath() != "/") {
		return "", "", fmt.Errorf("代理地址主机无效，且不能包含路径、查询或片段")
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || host == "" || port == "" {
		return "", "", fmt.Errorf("代理地址必须显式指定端口，IPv6 必须使用方括号")
	}
	if strings.HasPrefix(u.Host, "[") && net.ParseIP(host) == nil {
		return "", "", fmt.Errorf("IPv6 地址无效")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", "", fmt.Errorf("代理端口必须在 1 到 65535 之间")
	}
	u.Host = net.JoinHostPort(strings.ToLower(host), strconv.Itoa(n))
	u.Path, u.RawPath = "", ""
	if u.User != nil {
		username := u.User.Username()
		password, hasPassword := u.User.Password()
		if proxyHasWhitespace(username) || proxyHasWhitespace(password) {
			return "", "", fmt.Errorf("代理认证信息不能包含空白或控制字符")
		}
		// Re-encode decoded credentials so equivalent escapes compare equal without
		// losing reserved characters in usernames or passwords.
		if hasPassword {
			u.User = url.UserPassword(username, password)
		} else {
			u.User = url.User(username)
		}
	}
	canonical := u.String()
	display := *u
	if display.User != nil {
		// Hide all userinfo; a username can itself be a credential.
		display.User = url.UserPassword("***", "***")
	}
	return canonical, display.String(), nil
}

func validateProxyPool(req proxyPoolRequest) (proxyValidationResponse, error) {
	if req.DefaultProtocol != "" && !proxySchemeSupported(req.DefaultProtocol) {
		return proxyValidationResponse{}, fmt.Errorf("default_protocol 无效")
	}
	if len(req.Existing) > proxyPoolLineLimit {
		return proxyValidationResponse{}, fmt.Errorf("existing 代理数量不能超过 1000")
	}
	existing := make(map[string]string, len(req.Existing))
	ids := make(map[string]bool, len(req.Existing))
	for _, entry := range req.Existing {
		normalized, _, err := normalizeProxyURL(entry.URL, "")
		if err != nil || strings.TrimSpace(entry.ID) == "" || proxyHasWhitespace(entry.ID) || ids[entry.ID] {
			return proxyValidationResponse{}, fmt.Errorf("existing 代理目录包含无效记录")
		}
		ids[entry.ID] = true
		if _, ok := existing[normalized]; !ok {
			existing[normalized] = entry.ID
		}
	}
	lines := strings.Split(req.Lines, "\n")
	// A final line terminator does not add a 1001st proxy line.
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > proxyPoolLineLimit {
		return proxyValidationResponse{}, fmt.Errorf("每批最多验证 1000 行")
	}
	response := proxyValidationResponse{Rows: make([]proxyValidationRow, 0, len(lines))}
	seen := make(map[string]bool, len(lines))
	for i, line := range lines {
		// Accept CRLF, but never silently repair whitespace inside an address.
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		row := proxyValidationRow{Line: i + 1}
		normalized, display, err := normalizeProxyURL(line, req.DefaultProtocol)
		if err != nil {
			row.Status, row.Error = "error", err.Error()
			response.Errors++
		} else if id, ok := existing[normalized]; ok || seen[normalized] {
			row.Status, row.Display, row.URLHash, row.DuplicateID = "duplicate", display, proxyURLHash(normalized), id
			response.Duplicates++
		} else {
			row.Status, row.URL, row.Display, row.URLHash = "valid", normalized, display, proxyURLHash(normalized)
			response.Valid++
		}
		if normalized != "" {
			seen[normalized] = true
		}
		response.Rows = append(response.Rows, row)
	}
	return response, nil
}

func handleProxyValidation(raw []byte) (pluginapi.ManagementResponse, error) {
	if len(raw) > proxyPoolBodyLimit {
		return managementError(http.StatusRequestEntityTooLarge, "代理验证请求不能超过 1 MiB")
	}
	var req proxyPoolRequest
	if json.Unmarshal(raw, &req) != nil || strings.TrimSpace(string(raw)) == "null" {
		return managementError(http.StatusBadRequest, "代理验证请求格式无效")
	}
	response, err := validateProxyPool(req)
	if err != nil {
		return managementError(http.StatusBadRequest, err.Error())
	}
	return quotaJSON(response)
}

func proxyURLHash(canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// Reuse the list's AuthGet for both source and proxy summary. Single-card refresh
// also reads its API key from the same bytes. Configured describes native auth
// state, not effective egress: global host proxy policy can override that state.
func populateAccountDetails(card *quotaCard, raw []byte) {
	var stored struct {
		Source   string `json:"credential_source"`
		ProxyID  string `json:"commandcode_proxy_id"`
		ProxyURL string `json:"proxy_url"`
	}
	if json.Unmarshal(raw, &stored) != nil || strings.TrimSpace(string(raw)) == "null" {
		card.ProxyReadError = true
		return
	}
	if stored.Source == "api_key" || stored.Source == "browser_authorization" {
		card.CredentialSource = stored.Source
	}
	card.ProxyID = stored.ProxyID
	card.ProxyConfigured = stored.ProxyURL != ""
	if stored.ProxyURL == "" {
		return
	}
	normalized, display, err := normalizeProxyURL(stored.ProxyURL, "")
	if err != nil {
		card.ProxyReadError = true
		card.ProxyDisplay = "代理地址无效"
		return
	}
	card.ProxyDisplay, card.ProxyURLHash = display, proxyURLHash(normalized)
}

func (m *Manager) accountDetails(ctx context.Context, entry pluginapi.HostAuthFileEntry) (quotaCard, []byte, error) {
	card := accountCard(entry)
	record, err := m.bridge.AuthGet(ctx, entry.AuthIndex)
	if err != nil {
		card.ProxyReadError = true
		return card, nil, err
	}
	populateAccountDetails(&card, record.JSON)
	return card, record.JSON, nil
}
