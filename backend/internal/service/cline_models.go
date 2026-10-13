package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Cline's standard catalog omits ClinePass and free model IDs. The official
// client's public recommendation feed supplies those entries separately.
func isOfficialClineModelsRequest(account *Account, req *http.Request) bool {
	return account != nil && account.IsCline() && account.Type == AccountTypeAPIKey &&
		req != nil && req.URL != nil && isOfficialClineHost(req.URL.String()) &&
		(req.URL.Port() == "" || req.URL.Port() == "443") && req.URL.Path == "/api/v1/models"
}

func (s *AccountTestService) completeClineModelCatalog(ctx context.Context, account *Account, modelsRequest *http.Request, body []byte, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	target := *modelsRequest.URL
	target.Path = "/api/v1/ai/cline/recommended-models"
	target.RawPath = ""
	target.RawQuery = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	// This feed is public. Keep account credentials and header overrides on the
	// authenticated model-list request, while retaining its proxy and TLS profile.
	req.Header.Set("Accept", "application/json")
	resp, err := s.doUpstreamModelsRequest(req, upstreamModelsProxyURL(account), account)
	if err != nil {
		return nil, fmt.Errorf("request Cline recommendation feed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("Cline recommendation feed returned HTTP %d", resp.StatusCode)
	}
	recommended, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read Cline recommendation feed: %w", err)
	}
	if int64(len(recommended)) > limit {
		return nil, fmt.Errorf("Cline recommendation feed exceeds %d bytes", limit)
	}
	merged, err := mergeClineModelCatalog(body, recommended)
	if err != nil {
		return nil, err
	}
	if int64(len(merged)) > limit {
		return nil, fmt.Errorf("combined Cline model list exceeds %d bytes", limit)
	}
	return merged, nil
}

func mergeClineModelCatalog(body, recommended []byte) ([]byte, error) {
	entries, err := extractUpstreamModelRawEntries(body)
	if err != nil {
		return nil, err
	}
	models, err := extractUpstreamModelIDs(body)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(models))
	for _, model := range models {
		seen[model] = true
	}
	var feed struct {
		Recommended []json.RawMessage `json:"recommended"`
		Free        []json.RawMessage `json:"free"`
		ClinePass   []json.RawMessage `json:"clinePass"`
	}
	if err := json.Unmarshal(recommended, &feed); err != nil {
		return nil, fmt.Errorf("parse Cline recommendation feed: %w", err)
	}
	valid, added := 0, 0
	for _, section := range [][]json.RawMessage{feed.Recommended, feed.Free, feed.ClinePass} {
		for _, raw := range section {
			var model struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(raw, &model) != nil {
				continue
			}
			id := strings.TrimSpace(model.ID)
			if id == "" {
				continue
			}
			valid++
			if seen[id] {
				continue
			}
			seen[id] = true
			entries = append(entries, raw)
			added++
		}
	}
	if valid == 0 {
		return nil, fmt.Errorf("Cline recommendation feed returned no models")
	}
	if added == 0 {
		return body, nil
	}
	return json.Marshal(struct {
		Data []json.RawMessage `json:"data"`
	}{Data: entries})
}
