package codeindex

import (
	"fmt"
	"os"
)

// Extractor pulls symbols (and, in M2, calls + imports) from a single
// source file. Each implementation is responsible for one language.
//
// QName convention across languages:
//   <relative-file-path>::<container>.<name>     (when container exists, e.g. Go method, TS class member)
//   <relative-file-path>::<name>                 (top-level symbol)
//
// Filename-scoped qualified names sidestep build-system awareness while
// still disambiguating two functions named Foo in different files.
type Extractor interface {
	Language() string
	Extract(file FileEntry, src []byte) ExtractResult
}

// ExtractResult bundles everything one file produces. Calls and Imports
// are populated only for languages whose extractor supports them; other
// languages return empty slices.
type ExtractResult struct {
	Symbols []Symbol
	Calls   []ExtractedCall
	Imports []ExtractedImport
	Err     error // non-fatal parse errors are captured here so the caller can log/skip the file
}

// ExtractedCall is the in-flight form of a call edge before it lands
// in the calls table. CalleeQName may resolve to "" when cross-module
// resolution is impossible — the agent then matches via callee_name.
type ExtractedCall struct {
	CallerQName string
	CalleeQName string
	CalleeName  string
	Line        int
}

// ExtractedImport is the in-flight form of an imports row.
type ExtractedImport struct {
	Target string
	Alias  string
	Line   int
}

// extractors registry. Filled by language-specific init() (see
// extract_go.go etc).
var extractors = map[string]Extractor{}

// RegisterExtractor wires a language extractor into the registry. Called
// from each extractor file's init().
func RegisterExtractor(e Extractor) { extractors[e.Language()] = e }

// LookupExtractor returns the registered extractor for the language or
// nil if unsupported (caller skips the file).
func LookupExtractor(lang string) Extractor { return extractors[lang] }

// ExtractFile reads one file from disk and runs its language extractor.
// Returns an empty ExtractResult with Err set when the language has no
// extractor or the file can't be read.
func ExtractFile(fe FileEntry) ExtractResult {
	ext := LookupExtractor(fe.Language)
	if ext == nil {
		return ExtractResult{Err: fmt.Errorf("codeindex: no extractor for %s", fe.Language)}
	}
	src, err := os.ReadFile(fe.AbsPath)
	if err != nil {
		return ExtractResult{Err: fmt.Errorf("codeindex: read %s: %w", fe.RelPath, err)}
	}
	return ext.Extract(fe, src)
}

// makeQName builds the canonical QName for a symbol. container may be
// empty for top-level symbols.
func makeQName(file, container, name string) string {
	if container == "" {
		return file + "::" + name
	}
	return file + "::" + container + "." + name
}
