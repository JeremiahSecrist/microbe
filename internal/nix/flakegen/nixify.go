package flakegen

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// nixify renders a Go value as a Nix literal. Supported types: string, int,
// bool, []string, []any, and map[string]{string,int,bool,any}. Attrset keys
// are rendered sorted for deterministic output.
func nixify(v any) string {
	return renderValue(v, 0)
}

func renderValue(v any, depth int) string {
	switch x := v.(type) {
	case string:
		return nixQuote(x)
	case int:
		return strconv.Itoa(x)
	case bool:
		return renderBool(x)
	case []string:
		return nixInlineList(x)
	case []any:
		return renderList(x, depth)
	case map[string]string:
		return renderMap(x, depth, nixQuote)
	case map[string]int:
		return renderMap(x, depth, strconv.Itoa)
	case map[string]bool:
		return renderMap(x, depth, renderBool)
	case map[string]any:
		return renderMap(x, depth, func(v any) string { return renderValue(v, depth+1) })
	default:
		panic(fmt.Sprintf("nixify: unsupported type %T", v))
	}
}

func nixQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); {
		switch {
		case s[i] == '\\' || s[i] == '"':
			b.WriteByte('\\')
			b.WriteByte(s[i])
			i++
		case s[i] == '$' && i+1 < len(s) && s[i+1] == '{':
			b.WriteString("\\${")
			i += 2
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	b.WriteByte('"')
	return b.String()
}

func nixInlineList(items []string) string {
	parts := make([]string, len(items))
	for i, s := range items {
		parts[i] = nixQuote(s)
	}
	return "[ " + strings.Join(parts, " ") + " ]"
}

func renderList(items []any, depth int) string {
	nested := false
	for _, it := range items {
		if isNested(it) {
			nested = true
			break
		}
	}
	if !nested {
		parts := make([]string, len(items))
		for i, it := range items {
			parts[i] = renderValue(it, 0)
		}
		return "[ " + strings.Join(parts, " ") + " ]"
	}
	var b strings.Builder
	b.WriteString("[\n")
	for _, it := range items {
		b.WriteString(strings.Repeat("  ", depth+1))
		b.WriteString(renderValue(it, depth+1))
		b.WriteString("\n")
	}
	b.WriteString(strings.Repeat("  ", depth))
	b.WriteString("]")
	return b.String()
}

func isNested(v any) bool {
	switch v.(type) {
	case []any, []string, map[string]string, map[string]int, map[string]bool, map[string]any:
		return true
	}
	return false
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func renderAttrset(keys []string, value func(string) string, depth int) string {
	if len(keys) == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteString("{\n")
	for _, k := range keys {
		b.WriteString(strings.Repeat("  ", depth+1))
		b.WriteString(attrKey(k))
		b.WriteString(" = ")
		b.WriteString(value(k))
		b.WriteString(";\n")
	}
	b.WriteString(strings.Repeat("  ", depth))
	b.WriteString("}")
	return b.String()
}

// attrKey renders a Nix attribute name: bare if it is a valid identifier,
// quoted otherwise.
func attrKey(k string) string {
	if isNixIdent(k) {
		return k
	}
	return nixQuote(k)
}

func isNixIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_' || r == '\'' || r == '-':
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

func renderBool(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// renderMap renders any map[string]T as a Nix attrset, using format to
// render each value.
func renderMap[T any](m map[string]T, depth int, format func(T) string) string {
	return renderAttrset(sortedKeys(m), func(k string) string {
		return format(m[k])
	}, depth)
}
