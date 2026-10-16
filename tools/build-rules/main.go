// Package main builds rule.dat (gob-encoded) from v2fly/domain-list-community data.
// Usage: go run ./tools/build-rules/main.go [--data=path/to/data] [--out=rule.dat]
package main

import (
	"bufio"
	"encoding/gob"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

type Entry struct {
	Type  string
	Value string
}

type ParsedList struct {
	Name      string
	Inclusion map[string]bool
	Entry     []Entry
}

type List struct {
	Name  string
	Entry []Entry
}

const (
	RuleTypeDomain  = "domain"
	RuleTypeInclude = "include"
)

func removeCommentAttr(line string) string {
	idx := strings.Index(line, "#")
	if idx != -1 {
		line = strings.TrimSpace(line[:idx])
	}
	idx = strings.Index(line, "@")
	if idx != -1 {
		line = strings.TrimSpace(line[:idx])
	}
	return line
}

func parseDomain(domain string) (*Entry, error) {
	entry := Entry{}
	kv := strings.Split(domain, ":")
	if len(kv) == 1 {
		entry.Type = RuleTypeDomain
		entry.Value = strings.ToLower(kv[0])
		return &entry, nil
	}
	if len(kv) == 2 {
		entry.Type = strings.ToLower(kv[0])
		entry.Value = strings.ToLower(kv[1])
		return &entry, nil
	}
	return nil, errors.New("Invalid format: " + domain)
}

func LoadFile(path string) (*List, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	list := &List{Name: strings.ToUpper(filepath.Base(path))}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		line = removeCommentAttr(line)
		if len(line) == 0 {
			continue
		}
		entry, err := parseDomain(line)
		if err != nil {
			return list, err
		}
		list.Entry = append(list.Entry, *entry)
	}
	return list, nil
}

func parseList(list *List, listRef map[string]*List) (*ParsedList, error) {
	pl := &ParsedList{
		Name:      list.Name,
		Inclusion: make(map[string]bool),
	}
	entryList := list.Entry
	hasInclude := true
	for hasInclude {
		newEntryList := make([]Entry, 0, len(entryList))
		for _, entry := range entryList {
			if entry.Type == RuleTypeInclude {
				refName := strings.ToUpper(entry.Value)
				if pl.Inclusion[refName] {
					continue
				}
				pl.Inclusion[refName] = true
				refList := listRef[refName]
				if refList == nil {
					return nil, errors.New(entry.Value + " not found.")
				}
				newEntryList = append(newEntryList, refList.Entry...)
			} else {
				newEntryList = append(newEntryList, entry)
				hasInclude = false
			}
		}
		entryList = newEntryList
	}
	pl.Entry = entryList
	return pl, nil
}

func readAllFromFile(dir string) (map[string]*ParsedList, error) {
	listRef := make(map[string]*List)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		list, err := LoadFile(path)
		if err != nil {
			return err
		}
		listRef[list.Name] = list
		return nil
	})
	if err != nil {
		return nil, err
	}

	allRules := make(map[string]*ParsedList, len(listRef))
	for _, l := range listRef {
		parsedList, err := parseList(l, listRef)
		if err != nil {
			log.Println("err on parsing list", err)
			continue
		}
		allRules[l.Name] = parsedList
	}
	return allRules, nil
}

func main() {
	dataDir := flag.String("data", "./third_party/domain-list-community/data", "data dir path")
	outPath := flag.String("out", "rule.dat", "output path")
	flag.Parse()

	pl, err := readAllFromFile(*dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read data: %v\n", err)
		os.Exit(1)
	}

	file, err := os.Create(*outPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create output: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()

	if err := gob.NewEncoder(file).Encode(pl); err != nil {
		fmt.Fprintf(os.Stderr, "failed to encode: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Built %s with %d lists from %s\n", *outPath, len(pl), *dataDir)
}
