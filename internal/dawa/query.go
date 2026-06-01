package dawa

// query.go exposes the cross-cutting query helpers the api layer needs for the
// generic collection pipeline (q=/fuzzy= free-text matching). They are thin
// exported wrappers over the unexported autocomplete matchers so the matching
// semantics (case- and diacritic-insensitive substring) stay identical to the
// /autocomplete endpoints.

// MatchQ reports whether q is a folded (case- and diacritic-insensitive)
// substring of tekst. An empty q matches everything.
func MatchQ(tekst, q string) bool { return matchQ(tekst, q) }

// FoldString folds case and Danish/Latin diacritics to an ASCII base, exposing
// the same normalisation the autocomplete q matcher uses.
func FoldString(s string) string { return foldString(s) }
