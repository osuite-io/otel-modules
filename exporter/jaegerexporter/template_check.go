package jaegerexporter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"time"

	"github.com/olivere/elastic/v7"
)

func (e *jaegerExporter) checkIndexTemplates(ctx context.Context) error {
	patterns, err := fetchTemplatePatterns(ctx, e.client)
	if err != nil {
		return fmt.Errorf("jaegerexporter: could not read index templates: %w", err)
	}

	date := time.Now().UTC().Format(e.cfg.DateLayout)
	targets := []string{
		e.spanIndexBase + "-" + date,
		e.serviceIndexBase + "-" + date,
	}

	for _, target := range targets {
		if !matchesAny(target, patterns) {
			return fmt.Errorf("jaegerexporter: no index template matches write index %q; "+
				"create a template whose index_patterns cover it before starting "+
				"(set check_index_template=false to skip this check)", target)
		}
	}
	return nil
}

func matchesAny(index string, patterns []string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, index); ok {
			return true
		}
	}
	return false
}

func fetchTemplatePatterns(ctx context.Context, client *elastic.Client) ([]string, error) {
	var patterns []string

	legacyBody, err := performGET(ctx, client, "/_template")
	if err != nil {
		return nil, err
	}
	if legacyBody != nil {
		var legacyResp map[string]struct {
			IndexPatterns []string `json:"index_patterns"`
		}
		if err := json.Unmarshal(legacyBody, &legacyResp); err != nil {
			return nil, err
		}
		for _, t := range legacyResp {
			patterns = append(patterns, t.IndexPatterns...)
		}
	}

	composableBody, err := performGET(ctx, client, "/_index_template")
	if err != nil {
		return nil, err
	}
	if composableBody != nil {
		var composableResp struct {
			IndexTemplates []struct {
				IndexTemplate struct {
					IndexPatterns []string `json:"index_patterns"`
				} `json:"index_template"`
			} `json:"index_templates"`
		}
		if err := json.Unmarshal(composableBody, &composableResp); err != nil {
			return nil, err
		}
		for _, t := range composableResp.IndexTemplates {
			patterns = append(patterns, t.IndexTemplate.IndexPatterns...)
		}
	}

	return patterns, nil
}

func performGET(ctx context.Context, client *elastic.Client, urlPath string) (json.RawMessage, error) {
	resp, err := client.PerformRequest(ctx, elastic.PerformRequestOptions{
		Method: http.MethodGet,
		Path:   urlPath,
	})
	if err != nil {
		var esErr *elastic.Error
		if errors.As(err, &esErr) && esErr.Status == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	return resp.Body, nil
}
