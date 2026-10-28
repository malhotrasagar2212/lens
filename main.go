package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	var pos []string
	jsonOut, beta, ai := false, false, false
	for _, a := range os.Args[1:] {
		switch a {
		case "--json":
			jsonOut = true
		case "--beta":
			beta = true
		case "--ai":
			ai = true
		default:
			pos = append(pos, a)
		}
	}

	var (
		changes   []Change
		churn     int
		resources []string
		err       error
	)

	switch {
	case len(pos) >= 2 && pos[0] == "pr":
		var prNum int
		fmt.Sscanf(pos[1], "%d", &prNum)
		if prNum <= 0 {
			fatal(fmt.Errorf("invalid PR number %q", pos[1]))
		}
		token := os.Getenv("GITHUB_TOKEN")
		if token == "" {
			fmt.Fprintln(os.Stderr, "lens: set GITHUB_TOKEN (repo read scope)")
			os.Exit(1)
		}
		res, err := explainPR(token, prNum, beta)
		if err != nil {
			fatal(err)
		}

		if jsonOut {
			b, _ := json.MarshalIndent(res, "", "  ")
			fmt.Println(string(b))
		} else {
			printPRReport(res)
		}
		if ai {
			if txt, err := copilotExplain(buildPromt(res.SourceList, res.Files)); err != nil {
				fmt.Fprintln(os.Stderr, "lens:", err)
			} else {
				fmt.Println("\n WHY THESE CHANGED")
				for _, line := range strings.Split(txt, "\n") {
					fmt.Println("  ", line)
				}
			}
		}
		for _, c := range res.Changes {
			if c.Severity == Breaking {
				os.Exit(1)
			}
		}
		return
	case len(pos) == 2:
		bi, e1 := os.Stat(pos[0])
		hi, e2 := os.Stat(pos[1])
		if e1 != nil {
			fatal(e1)
		}
		if e2 != nil {
			fatal(e2)
		}
		if bi.IsDir() || hi.IsDir() {
			changes, churn, resources, err = diffDirs(pos[0], pos[1])
		} else {
			changes, churn, resources, err = diffFiles(pos[0], pos[1])
		}

	default:
		fmt.Fprintln(os.Stderr, "usage:")
		fmt.Fprintln(os.Stderr, "  lens pr <NUMBER> [--json] [--beta]")
		fmt.Fprintln(os.Stderr, "  lens [--json] <baseDir> <headDir>")
		fmt.Fprintln(os.Stderr, "  lens [--json] <base.go> <head.go>")
		os.Exit(2)
	}

	if err != nil {
		fatal(err)
	}

	sort.Strings(resources)
	if jsonOut {
		printJSon(resources, churn, changes)
	} else {
		printReport(resources, churn, changes)
	}
	for _, c := range changes {
		if c.Severity == Breaking {
			os.Exit(1)
		}
	}
}

func printPRReport(r *PRResult) {
	bar := strings.Repeat("=", 64)
	fmt.Println(bar)
	fmt.Printf(" lens - downstream blast radius")
	fmt.Println(bar)
	fmt.Printf("  repo    : %s\n", r.Repo)
	fmt.Printf("  fan-out : %d magic-modules file(s) -> %d generated file(s)\n\n", r.SourceFiles, len(r.Files))
	order := []string{"resource", "data source", "list resource", "action", "registration", "test", "docs", "meta", "other"}
	byCat := map[string][]FileChange{}
	for _, f := range r.Files {
		byCat[f.Category] = append(byCat[f.Category], f)
	}
	fmt.Println(" CHANGED FILES")
	for _, cat := range order {
		fs := byCat[cat]
		if len(fs) == 0 {
			continue
		}
		fmt.Printf("  [%s]:\n", cat)
		if len(fs) > 12 {
			bySvc := map[string]int{}
			for _, f := range fs {
				bySvc[serviceOf(f.Filename)]++
			}
			svcs := make([]string, 0, len(bySvc))
			for s := range bySvc {
				svcs = append(svcs, s)
			}
			sort.Slice(svcs, func(i, j int) bool {
				return bySvc[svcs[i]] > bySvc[svcs[j]]
			})
			fmt.Printf(".    %d files across: ", len(fs))
			parts := []string{}
			for _, s := range svcs {
				parts = append(parts, fmt.Sprintf("%s(%d)", s, bySvc[s]))
			}
			fmt.Println(strings.Join(parts, ", "))
		} else {
			for _, f := range fs {
				mark := "~"
				switch f.Status {
				case "added":
					mark = "+"
				case "removed":
					mark = "-"
				}
				fmt.Printf("    %s %-52s +%d/-%d\n", mark, f.Filename, f.Additions, f.Deletions)
			}
		}
		fmt.Println()

		var breaking, review, safe []Change
		for _, c := range r.Changes {
			switch c.Severity {
			case Breaking:
				breaking = append(breaking, c)
			case Warning:
				review = append(review, c)
			default:
				safe = append(safe, c)
			}
		}
		fmt.Println(" BEHAVIORAL CHANGES (resource schemas)")
		section("   [BREAKING]", breaking)
		section("   [REVIEW]", review)
		section("   [SAFE/ ADDITIVE]", safe)
		if len(r.Changes) == 0 {
			fmt.Println("   none in resource schemas")
		}
	}
}
func diffDirs(baseDir, headDir string) ([]Change, int, []string, error) {
	var changes []Change
	churn := 0
	resset := map[string]bool{}

	walk := func(root string, fn func(rel string)) error {
		return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !isGeneratedResourceFile(d.Name()) {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			fn(rel)
			return nil
		})
	}

	headfiles := map[string]bool{}
	if err := walk(headDir, func(rel string) {
		headfiles[rel] = true
		c, ch, rs, e := diffFiles(filepath.Join(baseDir, rel), filepath.Join(headDir, rel))
		if e != nil {
			return
		}
		changes = append(changes, c...)
		churn += ch
		for _, r := range rs {
			resset[r] = true
		}
	}); err != nil {
		return nil, 0, nil, err
	}
	if err := walk(baseDir, func(rel string) {
		if !headfiles[rel] {
			return
		}
		baseRes, e := parseGeneratedFile(filepath.Join(baseDir, rel))
		if e != nil {
			return
		}
		for fn := range baseRes {
			name := cleanName(fn)
			changes = append(changes, Change{name, Breaking, fmt.Sprintf("resource `%s` was removed", name)})
			resset[name] = true
		}
	}); err != nil {
		return nil, 0, nil, err
	}

	return changes, churn, keys(resset), nil
}

func diffFiles(basePath, headPath string) ([]Change, int, []string, error) {
	baseRes := map[string]*ResourceSpec{}
	if fileExists(basePath) {
		r, err := parseGeneratedFile(basePath)
		if err != nil {
			return nil, 0, nil, err
		}
		baseRes = r
	}

	headRes, err := parseGeneratedFile(headPath)
	if err != nil {
		return nil, 0, nil, err
	}

	var changes []Change
	var names []string
	for fn, hr := range headRes {
		br := baseRes[fn]
		if br == nil {
			br = &ResourceSpec{Schema: map[string]*FieldSpec{}}
		}
		changes = append(changes, diffResources(br, hr)...)
		names = append(names, cleanName(fn))
	}
	for fn := range baseRes {
		if _, ok := headRes[fn]; !ok {
			name := cleanName(fn)
			changes = append(changes, Change{name, Breaking, fmt.Sprintf("resource `%s` was removed", name)})
			names = append(names, name)
		}
	}

	churn := 0
	if fileExists(basePath) {
		churn = lineChurn(basePath, headPath)
	} else {
		churn = countLines(headPath)
	}
	return changes, churn, names, nil
}

func printReport(resources []string, churn int, changes []Change) {
	var breaking, review, safe []Change
	for _, c := range changes {
		switch c.Severity {
		case Breaking:
			breaking = append(breaking, c)
		case Warning:
			review = append(review, c)
		default:
			safe = append(safe, c)
		}
	}
	bar := strings.Repeat("=", 60)
	fmt.Println(bar)
	fmt.Println(" Generated Behavioral diff")
	fmt.Println(bar)
	if len(resources) > 0 {
		fmt.Printf(" resources: %s\n", strings.Join(resources, ", "))
	}
	fmt.Printf(" ~%s lines of generated Go changed -> %d behavioral change(s)\n", commafy(churn), len(changes))
	fmt.Println()
	section("[BREAKING]", breaking)
	section("[REVIEW]", review)
	section("[SAFE /ADDITIVE]", safe)
	if len(breaking) == 0 && len(review) == 0 {
		fmt.Println(" No breaking or review changes -  the diff is additive/cosmetic.")
	}
}

func section(title string, cs []Change) {
	if len(cs) == 0 {
		return
	}
	fmt.Printf("%s (%d)\n", title, len(cs))
	for _, c := range cs {
		fmt.Printf(" -%s\n", c.Message)
	}
	fmt.Println()
}

func printJSon(resources []string, churn int, changes []Change) {
	b, _ := json.MarshalIndent(struct {
		Resources  []string `json:"resources"`
		ChurnLines int      `json:"churn_lines"`
		Changes    []Change `json:"changes"`
	}{resources, churn, changes}, "", "  ")
	fmt.Println(string(b))
}

func isGeneratedResourceFile(name string) bool {
	return strings.HasSuffix(name, ".go") && (strings.HasPrefix(name, "resource_") || strings.HasPrefix(name, "data_source_"))
}

func cleanName(fn string) string {
	s := strings.TrimPrefix(fn, "Resource")
	s = strings.TrimPrefix(s, "DataSource")
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteRune('_')
			}
			b.WriteRune(r + 32)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func readLines(p string) []string {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	return strings.Split(string(b), "\n")
}

func countLines(p string) int { return len(readLines(p)) }

func lineChurn(a, b string) int {
	la, lb := readLines(a), readLines(b)
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

func commafy(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	return strings.Join(append([]string{s}, parts...), ",")
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}

func serviceOf(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if p == "services" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	if len(parts) > 1 {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}
