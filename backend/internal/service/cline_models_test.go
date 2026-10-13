package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func clineModelsResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func clineModelsAccount(baseURL string) *Account {
	return &Account{ID: 8016, Platform: PlatformCline, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-secret", "base_url": baseURL}}
}

func TestClineModelsIncludePassAndFreeWithoutLosingPaidMetadata(t *testing.T) {
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		clineModelsResponse(200, `{"object":"list","data":[{"id":"openai/gpt-6.1-sol","context_window":272000},{"id":"paid-only"}]}`),
		clineModelsResponse(200, `{"recommended":[{"id":"openai/gpt-6.1-sol","context_window":1}],"free":[{"id":"cline-free/step-5-preview"},{"id":""},{"name":"not-a-model-id"}],"clinePass":[{"id":"cline-pass/glm-5.3"},{"id":"cline-pass/glm-5.3"}],"clineCloud":[{"id":"cline-cloud/unsupported"}]}`),
	}}
	svc := &AccountTestService{httpUpstream: upstream, cfg: upstreamModelSyncTestConfig()}
	account := clineModelsAccount("https://api.cline.bot/api/v1")
	account.Credentials["header_override_enabled"] = true
	account.Credentials["header_overrides"] = map[string]any{"x-custom-secret": "private-value"}
	proxyID := int64(1)
	account.ProxyID = &proxyID
	account.Proxy = &Proxy{ID: 1, Protocol: "http", Host: "127.0.0.1", Port: 3128}
	models, body, err := svc.fetchUpstreamModelList(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, []string{"cline-free/step-5-preview", "cline-pass/glm-5.3", "openai/gpt-6.1-sol", "paid-only"}, models)
	require.Len(t, upstream.requests, 2)
	require.Equal(t, "https://api.cline.bot/api/v1/models", upstream.requests[0].URL.String())
	require.Equal(t, "Bearer test-secret", upstream.requests[0].Header.Get("Authorization"))
	require.Equal(t, "https://api.cline.bot/api/v1/ai/cline/recommended-models", upstream.requests[1].URL.String())
	require.Empty(t, upstream.requests[1].Header.Get("Authorization"))
	require.Equal(t, []string{"private-value"}, headerValuesEqualFold(upstream.requests[0].Header, "X-Custom-Secret"))
	require.Empty(t, headerValuesEqualFold(upstream.requests[1].Header, "X-Custom-Secret"))
	deadline, ok := upstream.requests[1].Context().Deadline()
	require.True(t, ok)
	require.LessOrEqual(t, time.Until(deadline), 5*time.Second)
	require.Equal(t, "http://127.0.0.1:3128", upstream.lastProxyURL)
	_, metadata, err := extractUpstreamModelCatalog(body, false)
	require.NoError(t, err)
	require.EqualValues(t, 272000, metadata["openai/gpt-6.1-sol"].ContextWindow)
	var envelope struct {
		Data []json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope))
	require.Len(t, envelope.Data, 4)
}

func TestClineModelsCustomUpstreamsKeepStandardDiscovery(t *testing.T) {
	for _, tc := range []struct{ platform, base string }{
		{PlatformCline, "https://proxy.example/api/v1"},
		{PlatformCline, "http://api.cline.bot/api/v1"},
		{PlatformCline, "https://api.cline.bot:8443/api/v1"},
		{PlatformCline, "https://api.cline.bot/custom/v1"},
		{PlatformOpenAI, "https://api.cline.bot/api/v1"},
	} {
		t.Run(tc.platform+"/"+tc.base, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: clineModelsResponse(200, `{"data":[{"id":"custom-model"}]}`)}
			cfg := upstreamModelSyncTestConfig()
			cfg.Security.URLAllowlist.AllowInsecureHTTP = true
			svc := &AccountTestService{httpUpstream: upstream, cfg: cfg}
			account := clineModelsAccount(tc.base)
			account.Platform = tc.platform
			models, err := svc.FetchUpstreamSupportedModels(context.Background(), account)
			require.NoError(t, err)
			require.Equal(t, []string{"custom-model"}, models)
			require.Len(t, upstream.requests, 1)
		})
	}
}

func TestClineModelsRejectIncompleteRecommendedCatalog(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"not found", 404, `{}`},
		{"unsupported", 405, `{}`},
		{"server error", 503, `{"error":"temporarily unavailable"}`},
		{"invalid JSON", 200, `{`},
		{"unknown structure", 200, `{"other":[]}`},
		{"empty catalog", 200, `{"recommended":[],"free":[],"clinePass":[]}`},
		{"invalid entries", 200, `{"clinePass":[{"name":"display name"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{responses: []*http.Response{clineModelsResponse(200, `{"data":[{"id":"paid-model"}]}`), clineModelsResponse(tc.status, tc.body)}}
			repo := &upstreamModelMetadataRepoStub{}
			svc := &AccountTestService{httpUpstream: upstream, cfg: upstreamModelSyncTestConfig(), accountRepo: repo}
			account := clineModelsAccount("https://api.cline.bot/api/v1")
			account.Credentials["model_mapping"] = map[string]any{"pass-alias": "cline-pass/glm-5.3"}
			upstream.resp = clineModelsResponse(503, `{}`)
			catalog, err := svc.SyncUpstreamModelCatalog(context.Background(), account)
			require.Error(t, err)
			require.Nil(t, catalog)
			require.Nil(t, repo.updates)
		})
	}
}

func TestClineModelsRecommendedResponseRespectsReadLimit(t *testing.T) {
	cfg := upstreamModelSyncTestConfig()
	cfg.Gateway.ModelsListReadMaxBytes = 100
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		clineModelsResponse(200, `{"data":[{"id":"paid-model"}]}`),
		clineModelsResponse(200, `{"clinePass":[{"id":"cline-pass/`+strings.Repeat("x", 100)+`"}]}`),
	}}
	svc := &AccountTestService{httpUpstream: upstream, cfg: cfg}
	models, err := svc.FetchUpstreamSupportedModels(context.Background(), clineModelsAccount("https://api.cline.bot/api/v1"))
	require.Error(t, err)
	require.Nil(t, models)
}

func TestClineModelsCombinedResponseRespectsReadLimit(t *testing.T) {
	cfg := upstreamModelSyncTestConfig()
	cfg.Gateway.ModelsListReadMaxBytes = 110
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		clineModelsResponse(200, `{"data":[{"id":"paid-model","description":"`+strings.Repeat("x", 40)+`"}]}`),
		clineModelsResponse(200, `{"clinePass":[{"id":"cline-pass/glm-5.3","description":"`+strings.Repeat("x", 20)+`"}]}`),
	}}
	svc := &AccountTestService{httpUpstream: upstream, cfg: cfg}
	models, err := svc.FetchUpstreamSupportedModels(context.Background(), clineModelsAccount("https://api.cline.bot/api/v1"))
	require.ErrorContains(t, err, "combined Cline model list")
	require.Nil(t, models)
}

func TestClineModelsPrimaryFailureDoesNotFetchRecommendations(t *testing.T) {
	upstream := &httpUpstreamRecorder{resp: clineModelsResponse(401, `{"error":"invalid key"}`)}
	svc := &AccountTestService{httpUpstream: upstream, cfg: upstreamModelSyncTestConfig()}
	_, err := svc.FetchUpstreamSupportedModels(context.Background(), clineModelsAccount("https://api.cline.bot/api/v1"))
	require.Error(t, err)
	require.Equal(t, 401, upstreamModelSyncStatusCode(err))
	require.Len(t, upstream.requests, 1)
}

func TestClineModelsRecommendedReadFailureClosesResponse(t *testing.T) {
	body := &passthroughCloseTrackingReadCloser{Reader: passthroughErrReadCloser{err: io.ErrUnexpectedEOF}}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		clineModelsResponse(200, `{"data":[{"id":"paid-model"}]}`),
		{StatusCode: 200, Body: body},
	}}
	svc := &AccountTestService{httpUpstream: upstream, cfg: upstreamModelSyncTestConfig()}
	_, err := svc.FetchUpstreamSupportedModels(context.Background(), clineModelsAccount("https://api.cline.bot/api/v1"))
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.True(t, body.closed)
}

func TestClineModelsCanceledRequestDoesNotReturnPartialCatalog(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	upstream := &codexModelsHTTPUpstreamStub{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		calls++
		if calls == 1 {
			cancel()
			return clineModelsResponse(200, `{"data":[{"id":"paid-model"}]}`), nil
		}
		return nil, req.Context().Err()
	}}
	svc := &AccountTestService{httpUpstream: upstream, cfg: upstreamModelSyncTestConfig()}
	models, err := svc.FetchUpstreamSupportedModels(ctx, clineModelsAccount("https://api.cline.bot/api/v1"))
	require.True(t, errors.Is(err, context.Canceled))
	require.Nil(t, models)
}

func TestClineModelsEmptyStandardCatalogCanDiscoverPassModels(t *testing.T) {
	for _, body := range []string{`{"data":[]}`, `{"models":[]}`, `[]`} {
		t.Run(body, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				clineModelsResponse(200, body),
				clineModelsResponse(200, `{"clinePass":[{"id":"cline-pass/glm-5.3"}]}`),
			}}
			svc := &AccountTestService{httpUpstream: upstream, cfg: upstreamModelSyncTestConfig()}
			models, err := svc.FetchUpstreamSupportedModels(context.Background(), clineModelsAccount(""))
			require.NoError(t, err)
			require.Equal(t, []string{"cline-pass/glm-5.3"}, models)
		})
	}
}

func TestClineModelsRejectInvalidStandardCatalog(t *testing.T) {
	for _, body := range []string{
		`null`, " \n null \t", `[null]`, `{"data":[null]}`, `{"data":[{}]}`,
		`{"data":[{"id":""}]}`, `{"data":null}`, `{"error":"unavailable"}`,
	} {
		t.Run(body, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				clineModelsResponse(200, body),
				clineModelsResponse(200, `{"clinePass":[{"id":"cline-pass/glm-5.3","context_window":128000,"reasoning":false,"input_modalities":["text"]}]}`),
			}, resp: clineModelsResponse(503, `{}`)}
			repo := &upstreamModelMetadataRepoStub{}
			svc := &AccountTestService{httpUpstream: upstream, cfg: upstreamModelSyncTestConfig(), accountRepo: repo}
			catalog, err := svc.SyncUpstreamModelCatalog(context.Background(), clineModelsAccount(""))
			require.Error(t, err)
			require.Nil(t, catalog)
			require.Nil(t, repo.updates)
		})
	}
}
