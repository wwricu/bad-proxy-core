package router

import (
	"encoding/gob"
	"os"
)

type RuleType string

const (
	FULL    RuleType = "full"
	DOMAIN  RuleType = "domain"
	KEYWORD RuleType = "keyword"
	REGEXP  RuleType = "regexp"
	RULE    RuleType = "rule"
)

type Entry struct {
	Type  string // full, domain, keyword, regexp, include
	Value string
}

type List struct {
	Name  string
	Entry []Entry
}

type ParsedList struct {
	Name      string
	Inclusion map[string]bool
	Entry     []Entry
}

func readAllFromGob(gobName string) (allRules map[string]*ParsedList, err error) {
	file, err := os.Open(gobName)
	if err != nil {
		return
	}
	err = gob.NewDecoder(file).Decode(&allRules)
	return
}
