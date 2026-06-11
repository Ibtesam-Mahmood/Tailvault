package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Tagged resolves a specific release by tag (for `tailvault update --version`).
// It accepts the tag with or without a leading "v".
func (c *Client) Tagged(ctx context.Context, tag string) (Release, error) {
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	url := fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s", c.base(), Owner, Repo, tag)
	req, err := c.authReq(ctx, http.MethodGet, url, "application/vnd.github+json")
	if err != nil {
		return Release{}, err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("query release %s: %w", tag, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Release{}, fmt.Errorf("no release tagged %s", tag)
	}
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("GitHub API returned %s", resp.Status)
	}
	var body struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("decode release JSON: %w", err)
	}
	rel := Release{Tag: body.TagName}
	for _, a := range body.Assets {
		rel.Assets = append(rel.Assets, Asset{Name: a.Name, APIURL: a.URL})
	}
	return rel, nil
}
