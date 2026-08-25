package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCodexQuotaOverdraftInjectorInjectsOneBoundedToolPair(t *testing.T) {
	injector := newCodexQuotaOverdraftInjector()
	injector.newCallID = func() (string, bool) { return "call_cpa_overdraft_test", true }
	original := []byte(`{"model":"gpt-5.4","store":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`)

	updated, changed := injector.inject(original)
	require.True(t, changed)
	require.NotEmpty(t, updated)
	require.Equal(t, `{"model":"gpt-5.4","store":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`, string(original))

	var document struct {
		Model string            `json:"model"`
		Store bool              `json:"store"`
		Input []json.RawMessage `json:"input"`
	}
	require.NoError(t, json.Unmarshal(updated, &document))
	require.Equal(t, "gpt-5.4", document.Model)
	require.False(t, document.Store)
	require.Len(t, document.Input, 3)

	var call struct {
		Type   string `json:"type"`
		Name   string `json:"name"`
		CallID string `json:"call_id"`
		Input  string `json:"input"`
	}
	var output struct {
		Type   string              `json:"type"`
		CallID string              `json:"call_id"`
		Output []map[string]string `json:"output"`
	}
	require.NoError(t, json.Unmarshal(document.Input[1], &call))
	require.NoError(t, json.Unmarshal(document.Input[2], &output))
	require.Equal(t, "custom_tool_call", call.Type)
	require.Equal(t, "exec", call.Name)
	require.Equal(t, "call_cpa_overdraft_test", call.CallID)
	require.Equal(t, codexQuotaOverdraftExecInput, call.Input)
	require.Equal(t, "custom_tool_call_output", output.Type)
	require.Equal(t, call.CallID, output.CallID)
	require.Len(t, output.Output, 1)
	require.Equal(t, "input_text", output.Output[0]["type"])
	require.NotEmpty(t, output.Output[0]["text"])
}

func TestReplaceTopLevelJSONFieldValuePreservesUnknownFieldsAndNestedInput(t *testing.T) {
	original := []byte(`{"note":"the string contains \"input\": false","meta":{"input":[1]},"input": [ {"type":"message","role":"user"} ],"tail":{"keep":true}}`)
	replacement := []byte(`[{"type":"message","role":"user"},{"type":"custom_tool_call"}]`)
	updated, replaced := replaceTopLevelJSONFieldValue(original, "input", []byte(` [ {"type":"message","role":"user"} ]`), replacement)
	require.True(t, replaced)

	var document struct {
		Note  string            `json:"note"`
		Meta  map[string][]int  `json:"meta"`
		Input []json.RawMessage `json:"input"`
		Tail  map[string]bool   `json:"tail"`
	}
	require.NoError(t, json.Unmarshal(updated, &document))
	require.Len(t, document.Input, 2)
	require.Equal(t, 1, document.Meta["input"][0])
	require.NotEmpty(t, document.Note)
	require.True(t, document.Tail["keep"])
}

func TestCodexQuotaOverdraftInjectorFailsOpenForUnsupportedRequests(t *testing.T) {
	valid := []byte(`{"input":[{"type":"message","role":"user","content":"continue"}]}`)
	tests := []struct {
		name    string
		body    []byte
		prepare func(*codexQuotaOverdraftInjector)
	}{
		{name: "invalid json", body: []byte(`{"input":`)},
		{name: "missing input", body: []byte(`{"model":"gpt-5.4"}`)},
		{name: "assistant is last", body: []byte(`{"input":[{"type":"message","role":"assistant","content":"done"}]}`)},
		{name: "already completed call is last", body: []byte(`{"input":[{"type":"message","role":"user"},{"type":"custom_tool_call_output","call_id":"existing"}]}`)},
		{name: "oversized", body: valid, prepare: func(injector *codexQuotaOverdraftInjector) { injector.maxBodyBytes = 8 }},
		{name: "call id unavailable", body: valid, prepare: func(injector *codexQuotaOverdraftInjector) {
			injector.newCallID = func() (string, bool) { return "", false }
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			injector := newCodexQuotaOverdraftInjector()
			if test.prepare != nil {
				test.prepare(injector)
			}
			updated, changed := injector.inject(test.body)
			require.False(t, changed)
			require.Empty(t, updated)
		})
	}
}

func TestOpenAIGatewayServiceApplyCodexQuotaOverdraftHonorsSwitchAndAccountType(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","role":"user","content":"continue"}]}`)
	repo := &codexQuotaOverdraftSettingRepo{value: `{"enabled":true}`}
	service := &OpenAIGatewayService{
		settingService: NewSettingService(repo, nil),
	}

	updated := service.applyCodexQuotaOverdraft(context.Background(), &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, body)
	var document struct {
		Input []json.RawMessage `json:"input"`
	}
	require.NoError(t, json.Unmarshal(updated, &document))
	require.Len(t, document.Input, 3)

	apiKeyBody := service.applyCodexQuotaOverdraft(context.Background(), &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, body)
	require.Equal(t, string(body), string(apiKeyBody))

	repo.value = `{"enabled":false}`
	disabledBody := service.applyCodexQuotaOverdraft(context.Background(), &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, body)
	require.Equal(t, string(body), string(disabledBody))
}

func TestOpenAIGatewayServiceForwardSendsInjectedCodexOverdraftBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"usage":{"input_tokens":1,"output_tokens":1}}`)),
	}}
	service := &OpenAIGatewayService{
		cfg:            &config.Config{},
		httpUpstream:   upstream,
		settingService: NewSettingService(&codexQuotaOverdraftSettingRepo{value: `{"enabled":true}`}, nil),
	}
	account := &Account{
		ID:       100,
		Name:     "codex-oauth",
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-account",
		},
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
	body := []byte(`{"model":"gpt-5.4","stream":false,"input":[{"type":"message","role":"user","content":"continue"}]}`)

	result, err := service.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)
	var forwarded struct {
		Input []json.RawMessage `json:"input"`
	}
	require.NoError(t, json.Unmarshal(upstream.lastBody, &forwarded))
	require.Len(t, forwarded.Input, 3)
}

func TestOpenAIGatewayServiceHTTPBridgeSendsInjectedCodexOverdraftBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_bridge\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n",
		)),
	}}
	service := &OpenAIGatewayService{
		cfg:          &config.Config{},
		httpUpstream: upstream,
		settingService: NewSettingService(&codexQuotaOverdraftSettingRepo{
			value: `{"enabled":true}`,
		}, nil),
	}
	account := &Account{
		ID:       101,
		Name:     "codex-oauth-bridge",
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-account",
		},
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	payload := []byte(`{"type":"response.create","model":"gpt-5.4","input":[{"type":"message","role":"user","content":"continue"}]}`)

	result, err := service.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "oauth-token", payload, len(payload),
		"gpt-5.4", "", "", "", "", 1,
		func([]byte) error { return nil },
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	var forwarded struct {
		Input []json.RawMessage `json:"input"`
	}
	require.NoError(t, json.Unmarshal(upstream.lastBody, &forwarded))
	require.Len(t, forwarded.Input, 3)
}

type codexQuotaOverdraftSettingRepo struct {
	value string
}

func (r *codexQuotaOverdraftSettingRepo) Get(context.Context, string) (*Setting, error) {
	return nil, ErrSettingNotFound
}

func (r *codexQuotaOverdraftSettingRepo) GetValue(context.Context, string) (string, error) {
	if r.value == "" {
		return "", ErrSettingNotFound
	}
	return r.value, nil
}

func (r *codexQuotaOverdraftSettingRepo) Set(context.Context, string, string) error {
	return nil
}

func (r *codexQuotaOverdraftSettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return nil, nil
}

func (r *codexQuotaOverdraftSettingRepo) SetMultiple(context.Context, map[string]string) error {
	return nil
}

func (r *codexQuotaOverdraftSettingRepo) GetAll(context.Context) (map[string]string, error) {
	return nil, nil
}

func (r *codexQuotaOverdraftSettingRepo) Delete(context.Context, string) error {
	return nil
}
