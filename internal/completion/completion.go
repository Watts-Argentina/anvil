// Package completion suggests SQL keywords, tables and columns for the word
// being typed in the SQL editor (SPEC §5.1).
package completion

import (
	"maps"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Watts-Argentina/anvil/internal/drivers"
)

// Limit is the maximum number of candidates Complete returns.
const Limit = 50

// Word is the identifier being typed before the cursor.
type Word struct {
	Start     int    // byte offset in the line where Prefix starts
	Prefix    string // text between Start and the cursor
	Qualifier string // word before a "." right before Prefix ("" if none)
}

// Kind is what a candidate names.
type Kind int

const (
	Column Kind = iota
	Table
	Keyword
)

func (k Kind) String() string {
	switch k {
	case Column:
		return "column"
	case Table:
		return "table"
	}
	return "keyword"
}

type Candidate struct {
	Text string
	Kind Kind
}

// Source is what can be suggested.
type Source struct {
	Keywords []string
	Tables   []drivers.Table
	// Columns of the tables whose columns are known, keyed by the
	// lower-case table name.
	Columns map[string][]drivers.Column
}

func isIdent(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// wordStart returns where the identifier that ends at col starts.
func wordStart(line string, col int) int {
	start := col
	for start > 0 {
		r, n := utf8.DecodeLastRuneInString(line[:start])
		if !isIdent(r) {
			break
		}
		start -= n
	}
	return start
}

// WordAt returns the word before byte column col of line.
func WordAt(line string, col int) Word {
	col = min(max(col, 0), len(line))
	w := Word{Start: wordStart(line, col)}
	w.Prefix = line[w.Start:col]
	if w.Start > 0 && line[w.Start-1] == '.' {
		// shortcut: quoted qualifiers ("t".col) are not recognized.
		qs := wordStart(line, w.Start-1)
		w.Qualifier = line[qs : w.Start-1]
	}
	return w
}

// Complete returns the candidates for w, columns first, then tables and
// keywords. After "x.", only the columns of table x (or the tables of
// schema x) are suggested; if x is neither, it is taken as an alias and the
// columns of every known table are suggested.
func Complete(w Word, src Source) []Candidate {
	m := matcher{prefix: strings.ToLower(w.Prefix), seen: map[string]bool{}}
	if q := strings.ToLower(w.Qualifier); q != "" {
		if cols, ok := src.Columns[q]; ok {
			m.columns(cols)
			return m.out
		}
		schema := false
		for _, t := range src.Tables {
			if strings.EqualFold(t.Schema, q) {
				schema = true
				m.add(t.Name, Table)
			}
		}
		if !schema {
			m.allColumns(src.Columns)
		}
		return m.out
	}
	if m.prefix == "" {
		return nil
	}
	m.allColumns(src.Columns)
	for _, t := range src.Tables {
		m.add(t.Name, Table)
	}
	lower := w.Prefix == strings.ToLower(w.Prefix)
	for _, k := range src.Keywords {
		if lower {
			k = strings.ToLower(k)
		}
		m.add(k, Keyword)
	}
	return m.out
}

type matcher struct {
	prefix string
	seen   map[string]bool
	out    []Candidate
}

func (m *matcher) columns(cols []drivers.Column) {
	for _, c := range cols {
		m.add(c.Name, Column)
	}
}

// allColumns adds the columns of every table, in table name order.
func (m *matcher) allColumns(cols map[string][]drivers.Column) {
	for _, t := range slices.Sorted(maps.Keys(cols)) {
		m.columns(cols[t])
	}
}

// add keeps text if it starts with the prefix (ignoring case), is not the
// prefix itself and was not added yet.
func (m *matcher) add(text string, k Kind) {
	l := strings.ToLower(text)
	if len(m.out) >= Limit || l == m.prefix || m.seen[l] || !strings.HasPrefix(l, m.prefix) {
		return
	}
	m.seen[l] = true
	m.out = append(m.out, Candidate{Text: text, Kind: k})
}

// Mentioned returns the tables whose name appears as a word in text.
func Mentioned(text string, tables []drivers.Table) []drivers.Table {
	words := map[string]bool{}
	for _, f := range strings.FieldsFunc(text, func(r rune) bool { return !isIdent(r) }) {
		words[strings.ToLower(f)] = true
	}
	var out []drivers.Table
	for _, t := range tables {
		if words[strings.ToLower(t.Name)] {
			out = append(out, t)
		}
	}
	return out
}
