package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestQuotaRecoveryTimeoutConsumesTicketAndReadsBack(t *testing.T) {
	entry := pluginapi.HostAuthFileEntry{ID: "stable", AuthIndex: "idx", Name: "a.json", Provider: ProviderID, Source: "file", Status: "error", Unavailable: true, StatusMessage: "weekly quota exhausted; account cooldown until 2099-01-01T00:00:00Z"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	defer close(release)
	var resets, reads atomic.Int32
	m := NewManager(NewHostBridge(func(method string, payload []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodHostAuthList:
			return hostOK(map[string]any{"files": []pluginapi.HostAuthFileEntry{entry}}), nil
		case pluginabi.MethodHostAuthGet:
			return hostOK(pluginapi.HostAuthGetResponse{AuthIndex: "idx", JSON: json.RawMessage(`{"type":"commandcode","api_key":"key-123"}`)}), nil
		case pluginabi.MethodHostAuthGetRuntime:
			reads.Add(1)
			return hostOK(pluginapi.HostAuthGetRuntimeResponse{Auth: entry}), nil
		case pluginabi.MethodHostRoutingResetCooldown:
			resets.Add(1)
			cancel()
			<-release
			return hostOK(pluginapi.HostRoutingResetCooldownResponse{AuthIndex: "idx"}), nil
		}
		return hostOK(map[string]any{}), nil
	}))
	m.cfg.BaseURL = "https://example.test/provider/v1"
	q := m.quotaState()
	q.fetch = func(context.Context, string, string, bool) (quotaSnapshot, error) { return healthyQuotaSnapshot(), nil }
	out, err := m.handleCooldownRecovery(context.Background(), []byte(`{"key_id":"idx","auth_id":"stable"}`))
	if err != nil || out.StatusCode >= 400 {
		t.Fatalf("preview: %s %v", out.Body, err)
	}
	var ticket struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(out.Body, &ticket); err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(quotaRecoveryRequest{KeyID: "idx", AuthID: "stable", Token: ticket.Token, Confirm: true})
	result, err := m.handleCooldownRecovery(ctx, request)
	if err != nil || result.StatusCode != http.StatusBadGateway || !strings.Contains(string(result.Body), "未知") {
		t.Fatalf("timeout: %s %v", result.Body, err)
	}
	if reads.Load() != 3 {
		t.Fatalf("timeout did not read back with a fresh context: %d", reads.Load())
	}
	again, _ := m.handleCooldownRecovery(context.Background(), request)
	if again.StatusCode != http.StatusConflict || resets.Load() != 1 {
		t.Fatal("timed-out reset was replayed")
	}
}
