package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

const mmRepo = "GoogleCloudPlatform/magic-modules"

const aiSystem = `You are an expert on Hashicorp's magic modules and the Terraform Google provider code-generation pipeline.
you are given the SOURCE files changed in magic-modules PR (with diffs) and the DOWNSTREAM generated provider files that resulted. Explain WHY the downstream files changed, grouped by which source change triggered them.

Rules:
- A change to a shared template (paths under mmv1/templates/ ending .tmpl) regenerates many files of the same kind across services - CLUB all those under ONE reason.
- A change to a specific resource (a product YAML, or handwritten code under a service dir) affects only resource'd files.
-  for each source trigger, output ONE bullet: the source file, then one cocise line on what changed and why it rippled, then the affectedservices with file counts.
-  Be terse and concrete. No preamble.`

type FileChange struct {
	Filename  string `json:"filename"`
	Category  string `json:"category"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

type PRResult struct {
	Repo        string       `json:"repo"`
	Base        string       `json:"base"`
	Head        string       `json:"head"`
	SourceFiles int          `json:"source_files"`
	Files       []FileChange `json:"files"`
	Changes     []Change     `json:"changes"`
	Resources   []string     `json:"resources"`
	SourceList  []srcFile    `json:"source_list"`
}

type srcFile struct {
	Filename string `json:"filename"`
	Patch    string `json:"patch"`
}

func buildPromt(source []srcFile, files []FileChange) string {
	var sb strings.Builder
	sb.WriteString(aiSystem)
	sb.WriteString("\n\nSOURCE FILES CHANGED IN THE MAGIC-MODULES PR:\n")
	for _, s := range source {
		sb.WriteString(fmt.Sprintf("=== ", s.Filename, " ===\n"))
		p := s.Patch
		if len(p) > 2000 {
			p = p[:2000] + "\n...[truncated]"
		}
		sb.WriteString(p + "\n\n")
		if sb.Len() > 300000 {
			sb.WriteString("...(more source files omitted)\n")
			break
		}
	}
	sb.WriteString("\nDOWNSTREAM GENERATED FILES:\n")
	for _, f := range files {
		sb.WriteString(fmt.Sprintf("%s (%s, +%d/-%d)\n", f.Filename, f.Status, f.Additions, f.Deletions))
	}
	return sb.String()
}

func copilotExplain(prompt string) (string, error) {
	cmd := exec.Command("copilot", "-p", prompt, "-s")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("copilot cli failed (%v): %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func prSourceFiles(token string, pr int) []srcFile {
	var all []srcFile
	for page := 1; ; page++ {
		var batch []srcFile
		u := fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d/files?per_page=100&page=%d", mmRepo, pr, page)
		if err := ghGetJSON(token, u, &batch); err != nil || len(batch) == 0 {
			break
		}
		all = append(all, batch...)
	}
	return all
}

func categorize(path string) string {
	b := baseName(path)
	switch {
	case strings.HasSuffix(path, ".markdown") || strings.Contains(path, "/docs/"):
		return "docs"
	case strings.HasSuffix(b, "_test.go"):
		return "test"
	case strings.HasPrefix(b, "resource_"):
		return "resource"
	case strings.HasPrefix(b, "list_"):
		return "list resource"
	case strings.HasPrefix(b, "action_"), strings.HasPrefix(b, "actions_"):
		return "action"
	case b == "registry.go" || strings.Contains(b, "provider.go"):
		return "registration"
	case strings.HasSuffix(b, ".yaml"):
		return "meta"
	default:
		return "other"
	}
}

// func countPRResourceFiles(token string, pr int) int {
// 	n := 0
// 	for page := 1; ; page++ {
// 		var batch []struct {
// 			Filename string `json:"filename"`
// 		}
// 		u := fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d/files?per_page=100&page=%d", mmRepo, pr, page)
// 		if err := ghGetJSON(token, u, &batch); err != nil || len(batch) == 0 {
// 			break
// 		}
// 		n += len(batch)
// 	}
// 	return n
//}

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
		Body      string `json:"body"`
		UpdatedAt string `json:"updated_at"`
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
	if sha := prHeadSHA(token, pr); len(sha) >= 7 {
		short := sha[:7]
		for _, c := range all {
			if !strings.Contains(c.Body, short) {
				continue
			}
			for _, m := range compareRe.FindAllStringSubmatch(c.Body, -1) {
				r, b, h := m[1], m[2], m[3]
				if strings.HasSuffix(r, "google-beta") == beta {
					return r, b, h, nil
				}
			}
		}
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].UpdatedAt > all[j].UpdatedAt
	})
	for _, c := range all {
		for _, m := range compareRe.FindAllStringSubmatch(c.Body, -1) {
			r, b, h := m[1], m[2], m[3]
			if strings.HasSuffix(r, "google-beta") == beta {
				return r, b, h, nil
			}
		}
	}
	return "", "", "", fmt.Errorf("no modular-magician compare link found on Pr #%d (beta=%v) -  is the Diff report posted yet?", pr, beta)
}

func fetchFileAt(token, repo, path, ref string) (string, bool, error) {
	u := fmt.Sprintf("https://api.github.com/repos/%s/contents/%s?ref=%s", repo, path, ref)
	req, _ := http.NewRequest("GET", u, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/vnd.github.raw")
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

func explainPR(token string, pr int, beta bool) (*PRResult, error) {
	repo, base, head, err := findCompareURL(token, pr, beta)
	if err != nil {
		return nil, err
	}

	var cmp struct {
		MergeBaseCommit struct {
			SHA string `json:"sha"`
		} `json:"merge_base_commit"`
		Files []struct {
			Filename  string `json:"filename"`
			Status    string `json:"status"`
			Additions int    `json:"additions"`
			Deletions int    `json:"deletions"`
			SHA       string `json:"sha"`
		} `json:"files"`
	}

	cmpURL := fmt.Sprintf("https://api.github.com/repos/%s/compare/%s...%s", repo, base, head)
	if err := ghGetJSON(token, cmpURL, &cmp); err != nil {
		return nil, err
	}
	changed := map[string]bool{}
	if mb := cmp.MergeBaseCommit.SHA; mb != "" && mb != base {
		var cmpB struct {
			Files []struct {
				Filename string `json:"filename"`
				SHA      string `json:"sha"`
			} `json:"files"`
		}
		bURL := fmt.Sprintf("https://api.github.com/repos/%s/compare/%s...%s", repo, mb, base)
		if err := ghGetJSON(token, bURL, &cmpB); err == nil {
			baseBlob := map[string]string{}
			for _, f := range cmpB.Files {
				baseBlob[f.Filename] = f.SHA
			}
			for _, f := range cmpB.Files {
				bsha, inBase := baseBlob[f.Filename]
				if !inBase || bsha != f.SHA {
					changed[f.Filename] = true
				}
			}
		}
	}

	src := prSourceFiles(token, pr)
	res := &PRResult{Repo: repo, Base: base, Head: head, SourceFiles: len(src), SourceList: src}
	resset := map[string]bool{}

	for _, f := range cmp.Files {
		if len(changed) > 0 && !changed[f.Filename] {
			continue
		}
		cat := categorize(f.Filename)
		res.Files = append(res.Files, FileChange{f.Filename, cat, f.Status, f.Additions, f.Deletions})

		if cat != "resource" && cat != "data source" {
			continue
		}

		baseSrc, hadBase, err := fetchFileAt(token, repo, f.Filename, base)
		if err != nil {
			return nil, err
		}
		headSrc, hadHead, err := fetchFileAt(token, repo, f.Filename, head)
		if err != nil {
			return nil, err
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
			res.Changes = append(res.Changes, diffResources(br, hr)...)
			resset[cleanName(fn)] = true
		}
		for fn := range baseRes {
			if _, ok := headRes[fn]; !ok {
				name := cleanName(fn)
				res.Changes = append(res.Changes, Change{name, Breaking, fmt.Sprintf("resource `%s` was removed", name)})
				resset[name] = true
			}
		}
	}
	res.Resources = keys(resset)
	sort.Strings(res.Resources)
	return res, nil
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

func prHeadSHA(token string, pr int) string {
	var d struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	u := fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d", mmRepo, pr)
	if err := ghGetJSON(token, u, &d); err != nil {
		return ""
	}
	return d.Head.SHA
}

func treeFiles(token, repo, sha string) map[string]string {
	var t struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"tree"`
	}

	u := fmt.Sprintf("https://api.github.com/repos/%s/git/trees/%s?recursive=1", repo, sha)
	if err := ghGetJSON(token, u, &t); err != nil {
		return nil
	}
	m := map[string]string{}
	for _, e := range t.Tree {
		m[e.Path] = e.SHA
	}
	return m
}
