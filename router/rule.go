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
	INCLUDE RuleType = "include"
	RULE    RuleType = "rule"
)

type Entry struct {
	Type  string // full, domain, keyword, regexp, include
	Value string
}

type ParsedList struct {
	Entry []Entry
}

func readAllFromGob(gobName string) (allRules map[string]*ParsedList, err error) {
	file, err := os.Open(gobName)
	if err != nil {
		return
	}
	defer file.Close()
	err = gob.NewDecoder(file).Decode(&allRules)
	return
}
