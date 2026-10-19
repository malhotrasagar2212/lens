package main

import (
	"encoding/json"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

const mmRepo = "GoogleCloudPlatform/magic-modules"

var comparere = regexp.MustCompile(`github\.com/(modular-magician/terraform-provider-google(?:-beta)?)/compare/([0-9a-f]+\.\.\.([0-9a-f]+)`)

func ghgetJSON(token, url string, v interface{}) error {
	req, _ := http.NewRequest("GET", url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-Github-Api-Version", "2022-11-28")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET %s: HTTP %d: %s", url, resp.StatusCode, body)
	}
	return json.Unmarshal(body, v)
}

func findCompareURL(token string, pr int, beta bool) (repo, base, head string, err error) {	
	type comment struct {
		Body string `json:"body"`	
	}
	var all []comment