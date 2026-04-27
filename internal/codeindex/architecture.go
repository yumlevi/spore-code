package codeindex

import (
	"path"
	"sort"
	"strings"
)

// Architecture summarizes the indexed codebase. Used by:
//   - the agent's `architecture` tool (returned as JSON)
//   - the project_memory_summary bootstrap line (collapsed counts)
//   - the eventual update_code_graph_summary server-side mirror
type Architecture struct {
	IndexHead   string                 `json:"index_head"`
	TechStack   []LanguageBreakdown    `json:"tech_stack"`
	EntryPoints []EntryPoint           `json:"entry_points"`
	Clusters    []Cluster              `json:"clusters"`
	HotPaths    []HotSymbol            `json:"hot_paths"`
	Stats       ArchitectureStats      `json:"stats"`
	Notes       []string               `json:"notes,omitempty"`
}

type LanguageBreakdown struct {
	Language string `json:"language"`
	Files    int    `json:"files"`
	Symbols  int    `json:"symbols"`
}

type EntryPoint struct {
	QName    string `json:"qname"`
	Name     string `json:"name"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Kind     string `json:"kind"`     // "main", "default-export", "init"
	Language string `json:"language"`
}

type Cluster struct {
	Name           string         `json:"name"`           // top-level directory or "(root)"
	Path           string         `json:"path"`           // directory path
	Files          int            `json:"files"`
	Symbols        int            `json:"symbols"`
	DominantLang   string         `json:"dominant_lang"`
	LanguageMix    map[string]int `json:"language_mix,omitempty"`
}

type HotSymbol struct {
	QName    string `json:"qname"`
	Name     string `json:"name"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Callers  int    `json:"callers"`
	Language string `json:"language"`
}

type ArchitectureStats struct {
	Files      int `json:"files"`
	Symbols    int `json:"symbols"`
	Functions  int `json:"functions"`
	Classes    int `json:"classes"`
	Methods    int `json:"methods"`
	Calls      int `json:"calls"`
	MaxCluster int `json:"max_cluster_files"`
}

// MaxClusters caps cluster count returned. Big monorepos tend to have
// many top-level dirs; the agent only needs the top N to orient itself.
const MaxClusters = 30

// MaxHotPaths caps hot-path entries. M1 returns 0 (no CALLS data yet);
// M2 surfaces top-N functions by inbound call count.
const MaxHotPaths = 20

// MaxEntryPoints caps entry-point list. Real projects rarely need more
// than a handful; if a repo has 50 main()s, the cap protects token use.
const MaxEntryPoints = 10

// Architecture computes the summary from the current store contents.
// Read-only — safe to call concurrently with reads, but callers should
// re-run after a fresh index commit to see updated numbers.
func ComputeArchitecture(s *Store) (*Architecture, error) {
	a := &Architecture{}

	// Stats from the existing helper.
	st, err := s.Stats()
	if err != nil {
		return nil, err
	}
	a.IndexHead = st.IndexHead
	a.Stats.Files = st.Files
	a.Stats.Symbols = st.Symbols

	// Symbol breakdown by kind.
	rows, err := s.db.Query(`SELECT kind, COUNT(*) FROM symbols GROUP BY kind`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			rows.Close()
			return nil, err
		}
		switch kind {
		case "function":
			a.Stats.Functions += n
		case "method":
			a.Stats.Methods += n
		case "class", "struct", "interface":
			a.Stats.Classes += n
		}
	}
	rows.Close()

	// Calls count (will be 0 in M1).
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM calls`).Scan(&a.Stats.Calls); err != nil {
		return nil, err
	}

	// Tech stack — file + symbol breakdown per language.
	a.TechStack = computeTechStack(s)

	// Clusters — by top-level directory of each indexed file.
	a.Clusters = computeClusters(s)
	if len(a.Clusters) > 0 {
		a.Stats.MaxCluster = a.Clusters[0].Files
	}

	// Entry points — Go main, init; JS/TS default exports named "main"
	// or matching common bootstrap shapes.
	a.EntryPoints = computeEntryPoints(s)

	// Hot paths — sorted by inbound CALLS edges. Empty in M1.
	a.HotPaths = computeHotPaths(s)

	// Notes — degraded coverage flags so the agent prompt knows when
	// to fall back to grep.
	a.Notes = computeCoverageNotes(a)

	return a, nil
}

func computeTechStack(s *Store) []LanguageBreakdown {
	type bd struct {
		files, symbols int
	}
	tally := map[string]*bd{}

	frows, err := s.db.Query(`SELECT language, COUNT(*) FROM files GROUP BY language`)
	if err != nil {
		return nil
	}
	for frows.Next() {
		var lang string
		var n int
		if err := frows.Scan(&lang, &n); err != nil {
			continue
		}
		tally[lang] = &bd{files: n}
	}
	frows.Close()

	srows, err := s.db.Query(`SELECT language, COUNT(*) FROM symbols GROUP BY language`)
	if err != nil {
		return nil
	}
	for srows.Next() {
		var lang string
		var n int
		if err := srows.Scan(&lang, &n); err != nil {
			continue
		}
		if t, ok := tally[lang]; ok {
			t.symbols = n
		} else {
			tally[lang] = &bd{symbols: n}
		}
	}
	srows.Close()

	out := make([]LanguageBreakdown, 0, len(tally))
	for lang, t := range tally {
		out = append(out, LanguageBreakdown{Language: lang, Files: t.files, Symbols: t.symbols})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Files != out[j].Files {
			return out[i].Files > out[j].Files
		}
		return out[i].Language < out[j].Language
	})
	return out
}

// computeClusters buckets files by their top-level directory. A cluster
// is one bucket; small repos may collapse to just the "(root)" cluster.
// Files in deeper paths are still owned by their top-level dir to keep
// the picture coarse and orientational.
func computeClusters(s *Store) []Cluster {
	rows, err := s.db.Query(`SELECT path, language, symbols_count FROM files`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	type bucket struct {
		path     string
		files    int
		symbols  int
		langs    map[string]int
	}
	buckets := map[string]*bucket{}

	for rows.Next() {
		var p, lang string
		var sym int
		if err := rows.Scan(&p, &lang, &sym); err != nil {
			continue
		}
		top := topDir(p)
		b, ok := buckets[top]
		if !ok {
			b = &bucket{path: top, langs: map[string]int{}}
			buckets[top] = b
		}
		b.files++
		b.symbols += sym
		b.langs[lang]++
	}

	out := make([]Cluster, 0, len(buckets))
	for _, b := range buckets {
		dom := dominantLang(b.langs)
		name := b.path
		if name == "" || name == "." {
			name = "(root)"
		}
		out = append(out, Cluster{
			Name: name, Path: b.path, Files: b.files, Symbols: b.symbols,
			DominantLang: dom, LanguageMix: b.langs,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Files != out[j].Files {
			return out[i].Files > out[j].Files
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > MaxClusters {
		out = out[:MaxClusters]
	}
	return out
}

// topDir returns the first path segment of a posix relative path.
// "internal/tools/exec.go" -> "internal"; "main.go" -> "" (root).
func topDir(p string) string {
	clean := path.Clean(p)
	if !strings.Contains(clean, "/") {
		return ""
	}
	return strings.SplitN(clean, "/", 2)[0]
}

func dominantLang(langs map[string]int) string {
	best := ""
	bestN := 0
	for l, n := range langs {
		if n > bestN || (n == bestN && l < best) {
			best, bestN = l, n
		}
	}
	return best
}

// computeEntryPoints surfaces obvious bootstrap symbols. Heuristic per
// language; conservative to keep the result short and trustworthy.
func computeEntryPoints(s *Store) []EntryPoint {
	rows, err := s.db.Query(`
		SELECT qname, name, file, start_line, kind, language
		FROM symbols
		WHERE
		  (language = 'go' AND name = 'main' AND kind = 'function')
		  OR (language = 'go' AND name = 'init' AND kind = 'function')
		  OR (language IN ('ts', 'js') AND name = 'main' AND kind = 'function')
		  OR (language IN ('ts', 'js') AND name = 'bootstrap' AND kind = 'function')
		ORDER BY language, file
	`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []EntryPoint
	for rows.Next() {
		var ep EntryPoint
		if err := rows.Scan(&ep.QName, &ep.Name, &ep.File, &ep.Line, &ep.Kind, &ep.Language); err != nil {
			continue
		}
		// Reclassify kind to a more agent-friendly label.
		switch ep.Name {
		case "main":
			ep.Kind = "main"
		case "init":
			ep.Kind = "init"
		case "bootstrap":
			ep.Kind = "bootstrap"
		}
		out = append(out, ep)
		if len(out) >= MaxEntryPoints {
			break
		}
	}
	return out
}

// computeHotPaths returns the top-N symbols by inbound CALLS edge count.
// Collect-then-resolve, never resolve inside the iterator: the store
// runs MaxOpenConns=1 and a nested QueryRow during rows.Next() would
// deadlock the pool.
func computeHotPaths(s *Store) []HotSymbol {
	rows, err := s.db.Query(`
		SELECT
		  COALESCE(NULLIF(c.callee_qname, ''), c.callee_name) AS callee,
		  c.callee_name,
		  COUNT(*) AS callers
		FROM calls c
		GROUP BY callee
		ORDER BY callers DESC
		LIMIT ?
	`, MaxHotPaths)
	if err != nil {
		return nil
	}
	type pending struct {
		qname, name string
		callers     int
	}
	var pendings []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.qname, &p.name, &p.callers); err != nil {
			continue
		}
		pendings = append(pendings, p)
	}
	rows.Close()

	out := make([]HotSymbol, 0, len(pendings))
	for _, p := range pendings {
		sym, _ := s.GetSymbol(p.qname)
		hs := HotSymbol{QName: p.qname, Name: p.name, Callers: p.callers}
		if sym != nil {
			hs.File = sym.File
			hs.Line = sym.StartLine
			hs.Language = sym.Language
		}
		out = append(out, hs)
	}
	return out
}

// computeCoverageNotes flags partial-coverage situations the agent should
// know about so the plan-mode prompt's "fall back to grep on misses"
// hint applies in the right places. As of v0.6.0 all extractors use
// real grammars (Go via stdlib go/ast, TS/JS/Python/Rust via
// tree-sitter), so the only remaining note is the "no CALLS yet"
// state when an index is brand-new.
func computeCoverageNotes(a *Architecture) []string {
	var notes []string
	if a.Stats.Calls == 0 && (a.Stats.Functions+a.Stats.Methods) > 0 {
		notes = append(notes, "no CALLS edges in this index — trace_calls / impact will return empty until the next /index pass populates them")
	}
	return notes
}
