// Package spiderbench runs the Spider text-to-SQL benchmark through the governed
// semantic layer and reports it HONESTLY: DataIntelligence only answers
// metric×dimension analytical queries, so most Spider questions (row-level
// lookups, rankings, sub-queries, set operations) are out of scope by design.
// The benchmark therefore reports *coverage* (how many questions are even
// expressible as a semantic query) alongside *correctness* on that slice —
// never a single leaderboard number that would misrepresent the trade.
//
// M1 (this file + expressible.go) needs only dev.json — no warehouse, no LLM.
// Download the Spider dev set (dev.json + tables.json + database/) e.g. from
// https://raw.githubusercontent.com/taoyds/spider/master/evaluation_examples/examples/
// and point `-data` at the directory that holds them.
package spiderbench

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Example is one Spider dev item. Query is the gold SQL string; SQL is Spider's
// pre-parsed structure (a nested tree of JSON arrays) which we classify directly,
// so no SQL parser is needed.
type Example struct {
	DBID     string         `json:"db_id"`
	Query    string         `json:"query"`
	Question string         `json:"question"`
	SQL      map[string]any `json:"sql"`
}

// LoadDev reads dev.json from a Spider data directory.
func LoadDev(dir string) ([]Example, error) {
	path := filepath.Join(dir, "dev.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w (download the Spider dev set first)", path, err)
	}
	var xs []Example
	if err := json.Unmarshal(b, &xs); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return xs, nil
}
