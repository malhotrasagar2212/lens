package main

import (
	"fmt"
	"sort"
)

type Severity int

const (
	Info Severity = iota
	Warning
	Breaking
)

func (s Severity) String() string {
	switch s {
	case Breaking:
		return "BREAKING"
	case Warning:
		return "REVIEW"
	default:
		return "SAFE"
	}
}

type Change struct {
	Path     string   `json:"path"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
}

func diffResources(base, head *ResourceSpec) []Change {
	var ch []Change
	ch = append(ch, diffSchema("", baseSchema, head.Schema)...)
	if base.HasImporter && !head.HasImporter {
		ch = append(ch, Change{"", Breaking, "import support was REMOVED"})
	}
	if !base.HasImporter && head.HasImporter {
		ch = append(ch, Change{"", Info, "import support was ADDED"})
	}
	if len(base.IDFormats) > 0 && len(head.IDFormats) > 0 && !sameStringSet(base.IDFormats, head.IDFormats) {
		ch = append(ch, Change{"", Warning, fmt.Sprintf("import/id format changed (best-effort): %v -> %v", base.IDFormats, head.IDFormats)})
	}
	return ch
}

func diffSchema(prefix string, base, head map[string]*FieldSpec) []Change {
	var ch []Change
	for name := range base {
		if _, ok := head[name]; !ok {
			p := joinPath(prefix, name)
			ch = append(ch, Change{p, Breaking, fmt.Sprintf("field %q was REMOVED", p)})
		}
	}
	for name, hf := range head {
		p := joinPath(prefix, name)
		bf, existed := base[name]
		if !existed {
			ch = append(ch, addedFieldChange(p, hf))
			continue
		}
		ch = append(ch, diffField(p, bf, hf)...)
	}
	sort.SliceStable(ch, func(i, j int) bool {
		if ch[i].Severity != ch[j].Severity {
			return ch[i].Severity > ch[j].Severity
		}
		return ch[i].Path < ch[j].Path
	})
	return ch
}

func addedFieldChange(p string, hf *FieldSpec) Change {
	switch {
	case hf.Required:
		return Change{p, Breaking, fmt.Sprintf("added REQUIRED field `%s` (%s) - existing configs without it will fail", p, typeName(hf))}
	case hf.Computed && !hf.Optional:
		return Change{p, Info, fmt.Sprintf("added output-onlyfield `%s` (%s)", p, typeName(hf))}
	default:
		return Change{p, Info, fmt.Sprintf("added optional field `%s` (%s)", p, typeName(hf))}
	}
}

func diffField(p string, bf, hf *FieldSpec) []Change {
	var ch []Change
	if !bf.ForceNew && hf.ForceNew {
		ch = append(ch, Change{p, Breaking, fmt.Sprintf("`%s` is now ForceNew -  changing it DESTROYS and recreates the resource", p)})
	}
	if bf.ForceNew && !hf.ForceNew {
		ch = append(ch, Change{p, Info, fmt.Sprintf("`%s`is no longer ForceNew", p)})
	}
	if bf.Required && !hf.Required {
		ch = append(ch, Change{p, Info, fmt.Sprintf("`%s` is now optional", p)})
	}
	if !bf.Required && hf.Required {
		ch = append(ch, Change{p, Breaking, fmt.Sprintf("`%s` is now required", p)})
	}
	if !bf.Computed && hf.Computed {
		ch = append(ch, Change{p, Warning, fmt.Sprintf("`%s` is now computed", p)})
	}
	if bf.Type != "" && hf.Type != "" && bf.Type != hf.Type {
		ch = append(ch, Change{p, Breaking, fmt.Sprintf("`%s` type changed from `%s` to `%s`", p, bf.Type, hf.Type)})
	}
	if bf.Sensitive != hf.Sensitive {
		if hf.Sensitive {
			ch = append(ch, Change{p, Info, fmt.Sprintf("`%s` is now sensitive", p)})
		} else {
			ch = append(ch, Change{p, Warning, fmt.Sprintf("`%s` is no longer sensitive", p)})
		}
	}
	if bf.MaxItems != hf.MaxItems {
		sev := Info
		if hf.MaxItems != 0 && (bf.MaxItems == 0 || hf.MaxItems < bf.MaxItems) {
			sev = Warning
		}
		ch = append(ch, Change{p, sev, fmt.Sprintf("`%s` MaxItems changed from `%d` to `%d`", p, bf.MaxItems, hf.MaxItems)})
	}
	added, removed := diffStringSets(bf.EnumValues, hf.EnumValues)
	for _, v := range added {
		ch = append(ch, Change{p, Info, fmt.Sprintf("`%s` accepts new value: `%s`", p, v)})
	}
	for _, v := range removed {
		ch = append(ch, Change{p, Breaking, fmt.Sprintf("`%s` no longer accepts value: `%s` - configs using it break", p, v)})
	}
	if bf.HasDefault != hf.HasDefault || bf.Default != hf.Default {
		ch = append(ch, Change{p, Warning, fmt.Sprintf("`%s` default changed from `%q` to `%q`", p, bf.Default, hf.Default)})
	}
	if bf.nested != nil && hf.nested != nil {
		ch = append(ch, diffSchema(p, mapOr(bf.Nested), mapOr(hf.Nested))...)
	}
	return ch
}

func typeName(f *FieldSpec) string {
	if f.Type == "" {
		return "?"
	}
	return f.Type
}

func joinPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

func mapOr(m map[string]*FieldSpec) map[string]*FieldSpec {
	if m == nil {
		return map[string]*FieldSpec{}
	}
	return m
}

func diffStringSets(base, head []string) (added, removed []string) {
	b := make(map[string]bool{})
	for _, v := range base {
		b[v] = true
	}
	h := make(map[string]bool{})
	for _, v := range head {
		h[v] = true
	}
	for v := range h {
		if !b[v] {
			added = append(added, v)
		}
	}
	for v := range b {
		if !h[v] {
			removed = append(removed, v)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return
}

func sameStringSet(a, b []string) bool {
	added, removed := diffStringSets(a, b)
	return len(added) == 0 && len(removed) == 0
}
