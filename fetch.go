package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

const mmRepo = "GoogleCloudPlatform/magic-modules"

var compareRe = regexp.MustCompile(`github\.com/(modular-magician/terraform-provider-google(?:-beta)?)/compare/([0-9a-f]+)\.\.\.?([0-9a-f]+)`)

func ghGetJSON(token, url string, v interface{}) error {
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

// findCompareURL scans the PR's comments for the bot's compare link for the
// chosen provider (google GA by default, google-beta if beta is true).
func findCompareURL(token string, pr int, beta bool) (repo, base, head string, err error) {
	type comment struct {
		Body string `json:"body"`
	}
	var all []comment
	for page := 1; ; page++ {
		var batch []comment
		u := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d/comments?per_page=100&page=%d", mmRepo, pr, page)
		if err = ghGetJSON(token, u, &batch); err != nil {
			return
		}
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
	}
	for _, c := range all {
		for _, m := range compareRe.FindAllStringSubmatch(c.Body, -1) {
			r, b, h := m[1], m[2], m[3]
			if strings.HasSuffix(r, "google-beta") == beta {
				return r, b, h, nil
			}
		}
	}
	// fmt.Printf("DEBUG: fetched %d comments; ", len(all))
	// ch := 0
	// for _, c := range all {
	// 	if strings.Contains(c.Body, "compare/") {
	// 		ch++
	// 	}
	// }
	// fmt.Printf("%d contain 'compare/'\n", ch)
	return "", "", "", fmt.Errorf("no modular-magician compare link found on Pr #%d (beta=%v) -  is the Diff report posted yet?", pr, beta)
}

func fetchFileAt(token, repo, path, ref string) (string, bool, error) {
	u := fmt.Sprintf("https://api.github.com/repos/%s/contents/%s?ref=%s", repo, path, ref)
	req, _ := http.NewRequest("GET", u, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return "", false, nil
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", false, fmt.Errorf("contents %s@%s: HTTP %d: %s", path, ref, resp.StatusCode, body)
	}
	return string(body), true, nil
}

func explainPR(token string, pr int, beta bool) ([]Change, int, []string, error) {
	repo, base, head, err := findCompareURL(token, pr, beta)
	if err != nil {
		return nil, 0, nil, err
	}

	var cmp struct {
		Files []struct {
			Filename string `json:"filename"`
		} `json:"files"`
	}

	cmpURL := fmt.Sprintf("https://api.github.com/repos/%s/compare/%s...%s", repo, base, head)
	if err := ghGetJSON(token, cmpURL, &cmp); err != nil {
		return nil, 0, nil, err
	}
	var changes []Change
	churn := 0
	resset := map[string]bool{}

	fmt.Printf("DEBUG: comparing %d files\n", len(cmp.Files))
	for _, f := range cmp.Files {
		fmt.Printf(" ", f.Filename)
	}

	for _, f := range cmp.Files {
		if !isGeneratedResourceFile(baseName(f.Filename)) {
			continue
		}
		baseSrc, hadBase, err := fetchFileAt(token, repo, f.Filename, base)
		if err != nil {
			return nil, 0, nil, err
		}
		headSrc, hadHead, err := fetchFileAt(token, repo, f.Filename, head)
		if err != nil {
			return nil, 0, nil, err
		}

		baseRes := map[string]*ResourceSpec{}
		if hadBase {
			baseRes = parseSource(baseSrc)
		}
		headRes := map[string]*ResourceSpec{}
		if hadHead {
			headRes = parseSource(headSrc)
		}

		for fn, hr := range headRes {
			br := baseRes[fn]
			if br == nil {
				br = &ResourceSpec{Schema: map[string]*FieldSpec{}}
			}
			changes = append(changes, diffResources(br, hr)...)
			resset[cleanName(fn)] = true
		}
		for fn := range baseRes {
			if _, ok := headRes[fn]; !ok {
				name := cleanName(fn)
				changes = append(changes, Change{name, Breaking, fmt.Sprintf("resource `%s` was removed", name)})
				resset[name] = true
			}
		}
		churn += srcChurn(baseSrc, headSrc)
	}
	return changes, churn, keys(resset), nil
}

func baseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func srcChurn(a, b string) int {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	n := len(la)
	if len(lb) > n {
		n = len(lb)
	}
	d := 0
	for i := 0; i < n; i++ {
		var x, y string
		if i < len(la) {
			x = la[i]
		}
		if i < len(lb) {
			y = lb[i]
		}
		if strings.TrimSpace(x) != strings.TrimSpace(y) {
			d++
		}
	}
	return d
}
