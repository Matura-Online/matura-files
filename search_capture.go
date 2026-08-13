package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	studySearchWorkers = 3
	studySearchDelay   = 80 * time.Millisecond
)

var studySearchCacheDir = filepath.Join(studyRefreshCacheDir, "search")

type capturedFilterOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}
type capturedFilterMenu struct {
	Default capturedFilterOption   `json:"default"`
	Options []capturedFilterOption `json:"options"`
}
type capturedFilterCatalog struct {
	ID    string                        `json:"id"`
	Menus map[string]capturedFilterMenu `json:"menus"`
}
type capturedFilterTransition struct {
	State      string   `json:"state"`
	Sastavnice []string `json:"sastavnice"`
	Mjesta     []string `json:"mjesta"`
	Podrucje   string   `json:"podrucje"`
}
type capturedFilterDocument struct {
	Selectors map[string]struct {
		Options []capturedFilterOption `json:"options"`
	} `json:"selectors"`
	Catalogs    []capturedFilterCatalog `json:"catalogs"`
	Transitions struct {
		Podrucje []capturedFilterTransition `json:"podrucje"`
	} `json:"transitions"`
}
type terminalSearchState struct {
	Key                            string
	Lista, Mjesta                  []string
	Mjesto, Podrucje, Polje, Kvota string
	Baseline                       bool
}
type terminalSearchResult struct {
	State    terminalSearchState
	Programs []rawProgram
	Calls    int
	Cached   bool
	Err      error
}
type programEvidence struct{ Components, Places, Areas, Fields, Quotas map[string]bool }

func captureStudySearchRelations(client *studyHTTPClient, filtersPath string) (map[int]studySearchRelation, error) {
	data, err := os.ReadFile(filtersPath)
	if err != nil {
		return nil, fmt.Errorf("read generated filters %s: %w", filtersPath, err)
	}
	var filters capturedFilterDocument
	if err := json.Unmarshal(data, &filters); err != nil {
		return nil, fmt.Errorf("decode generated filters: %w", err)
	}
	states, err := terminalStates(filters)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(studySearchCacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("create search cache: %w", err)
	}
	pending := make([]terminalSearchState, 0, len(states))
	preloaded := make([]terminalSearchResult, 0, len(states))
	for _, state := range states {
		result, ok, err := loadTerminalSearchCache(state)
		if err != nil {
			return nil, err
		}
		if ok {
			preloaded = append(preloaded, result)
		} else {
			pending = append(pending, state)
		}
	}
	fmt.Printf("Capturing %d terminal search states (%d cached, %d pending)\n", len(states), len(preloaded), len(pending))
	jobs := make(chan terminalSearchState)
	results := make(chan terminalSearchResult, studySearchWorkers)
	var wg sync.WaitGroup
	for worker := 0; worker < studySearchWorkers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for state := range jobs {
				results <- client.fetchTerminalSearch(state)
				time.Sleep(studySearchDelay)
			}
		}()
	}
	go func() {
		for _, state := range pending {
			jobs <- state
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()
	evidence := map[int]*programEvidence{}
	orders := map[string]map[int]int{}
	calls, done := 0, 0
	consume := func(result terminalSearchResult) error {
		if result.Err != nil {
			return result.Err
		}
		if !result.Cached {
			if err := writeTerminalSearchCache(result); err != nil {
				return err
			}
		}
		calls += result.Calls
		if result.State.Baseline {
			order := map[int]int{}
			for index, program := range result.Programs {
				order[program.IDPrograma] = index
			}
			if prior, exists := orders[result.State.Kvota]; exists && !equalSearchOrder(prior, order) {
				return fmt.Errorf("inconsistent baseline order for quota %s", result.State.Kvota)
			}
			orders[result.State.Kvota] = order
		}
		for _, program := range result.Programs {
			row := evidence[program.IDPrograma]
			if row == nil {
				row = &programEvidence{map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}}
				evidence[program.IDPrograma] = row
			}
			row.Places[program.Mjesto] = true
			if len(result.State.Lista) == 1 {
				row.Components[result.State.Lista[0]] = true
			}
			if result.State.Podrucje != "-1" {
				row.Areas[result.State.Podrucje] = true
			}
			if result.State.Polje != "-1" {
				row.Fields[result.State.Polje] = true
			}
			if result.State.Kvota != "-1" {
				row.Quotas[result.State.Kvota] = true
			}
		}
		done++
		studyProgress("search", done, len(states), fmt.Sprintf(" - %d HTTP pages", calls))
		return nil
	}
	for _, result := range preloaded {
		if err := consume(result); err != nil {
			return nil, err
		}
	}
	for result := range results {
		if err := consume(result); err != nil {
			return nil, err
		}
	}
	if len(orders) != 3 {
		return nil, fmt.Errorf("expected 3 quota baseline result sets, got %d", len(orders))
	}
	relations := map[int]studySearchRelation{}
	for id, row := range evidence {
		components, places, quotas := sortedSet(row.Components, true), sortedSet(row.Places, false), sortedSet(row.Quotas, true)
		if len(components) != 1 || len(places) != 1 || len(quotas) > 1 {
			return nil, fmt.Errorf("ambiguous relation for %d: components=%v places=%v quotas=%v", id, components, places, quotas)
		}
		quota := "-1"
		if len(quotas) == 1 {
			quota = quotas[0]
		}
		order, ok := orders[quota][id]
		if !ok {
			return nil, fmt.Errorf("program %d missing from quota %s baseline", id, quota)
		}
		relation := studySearchRelation{SastavnicaID: components[0], Podrucja: sortedSet(row.Areas, true), Polja: sortedSet(row.Fields, true), PosebnaKvota: quota, Redoslijed: order}
		if !relation.valid() {
			return nil, fmt.Errorf("incomplete relation for program %d", id)
		}
		relations[id] = relation
	}
	fmt.Printf("Captured %d program relations from %d HTTP result pages\n", len(relations), calls)
	return relations, nil
}

func terminalStates(filters capturedFilterDocument) ([]terminalSearchState, error) {
	catalogs := map[string]capturedFilterCatalog{}
	for _, catalog := range filters.Catalogs {
		catalogs[catalog.ID] = catalog
	}
	quotas := filters.Selectors["posebna_kvota"].Options
	if len(quotas) == 0 {
		return nil, fmt.Errorf("filters have no special quota options")
	}
	maxComponents, maxPlaces := 0, 0
	for _, transition := range filters.Transitions.Podrucje {
		if len(transition.Sastavnice) > maxComponents {
			maxComponents = len(transition.Sastavnice)
		}
		if len(transition.Mjesta) > maxPlaces {
			maxPlaces = len(transition.Mjesta)
		}
	}
	unique := map[string]terminalSearchState{}
	for _, transition := range filters.Transitions.Podrucje {
		catalog, ok := catalogs[transition.State]
		if !ok {
			return nil, fmt.Errorf("area transition references unknown catalog %s", transition.State)
		}
		fields, ok := catalog.Menus["polje"]
		if !ok {
			return nil, fmt.Errorf("catalog %s has no field menu", catalog.ID)
		}
		allFields := append([]capturedFilterOption{fields.Default}, fields.Options...)
		place := "Sva mjesta"
		if len(transition.Mjesta) == 1 && transition.Mjesta[0] != "-1" {
			place = transition.Mjesta[0]
		}
		for _, field := range allFields {
			for _, quota := range quotas {
				payload := map[string]any{"lista": transition.Sastavnice, "search": "", "searchVisokaUcilista": "", "Mjesto": place, "polje": field.Value, "podrucje": transition.Podrucje, "usporedba": true, "posebnaKvota": quota.Value, "page": 1}
				key := shortStudyFilterHash(payload)
				unique[key] = terminalSearchState{Key: key, Lista: append([]string(nil), transition.Sastavnice...), Mjesta: append([]string(nil), transition.Mjesta...), Mjesto: place, Podrucje: transition.Podrucje, Polje: field.Value, Kvota: quota.Value, Baseline: len(transition.Sastavnice) == maxComponents && len(transition.Mjesta) == maxPlaces && transition.Podrucje == "-1" && field.Value == "-1"}
			}
		}
	}
	states := make([]terminalSearchState, 0, len(unique))
	for _, state := range unique {
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Key < states[j].Key })
	if len(states) == 0 {
		return nil, fmt.Errorf("no terminal search states")
	}
	return states, nil
}

func (c *studyHTTPClient) fetchTerminalSearch(state terminalSearchState) terminalSearchResult {
	result := terminalSearchResult{State: state}
	payload := map[string]any{"lista": state.Lista, "search": "", "searchVisokaUcilista": "", "Mjesto": state.Mjesto, "polje": state.Polje, "podrucje": state.Podrucje, "usporedba": true, "posebnaKvota": state.Kvota, "page": 1}
	for page := 1; ; page++ {
		payload["page"] = page
		body, err := json.Marshal(payload)
		if err != nil {
			result.Err = err
			return result
		}
		var data []byte
		var status int
		for attempt := 1; attempt <= 3; attempt++ {
			data, status, err = c.do(http.MethodPost, programsAPIURL, body, ajaxHeaders(programsPageURL))
			if err == nil && status == http.StatusOK {
				break
			}
			if err == nil {
				err = fmt.Errorf("HTTP %d", status)
			}
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		if err != nil {
			result.Err = fmt.Errorf("state %s page %d: %w", state.Key, page, err)
			return result
		}
		var envelope struct {
			D struct {
				TotalPages int          `json:"TotalPages"`
				Programi   []rawProgram `json:"Programi"`
			} `json:"d"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			result.Err = err
			return result
		}
		if envelope.D.TotalPages < 1 {
			result.Err = fmt.Errorf("state %s has invalid page count", state.Key)
			return result
		}
		result.Programs = append(result.Programs, envelope.D.Programi...)
		result.Calls++
		if page >= envelope.D.TotalPages {
			return result
		}
		time.Sleep(studySearchDelay)
	}
}
func equalSearchOrder(left, right map[int]int) bool {
	if len(left) != len(right) {
		return false
	}
	for id, index := range left {
		if right[id] != index {
			return false
		}
	}
	return true
}
func sortedSet(values map[string]bool, numeric bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	if numeric {
		sort.Slice(result, func(i, j int) bool {
			var a, b int
			fmt.Sscanf(result[i], "%d", &a)
			fmt.Sscanf(result[j], "%d", &b)
			return a < b
		})
	} else {
		sort.Strings(result)
	}
	return result
}

func terminalSearchCachePath(state terminalSearchState) string {
	return filepath.Join(studySearchCacheDir, state.Key+".json")
}

func loadTerminalSearchCache(state terminalSearchState) (terminalSearchResult, bool, error) {
	data, err := os.ReadFile(terminalSearchCachePath(state))
	if os.IsNotExist(err) {
		return terminalSearchResult{}, false, nil
	}
	if err != nil {
		return terminalSearchResult{}, false, fmt.Errorf("read search cache %s: %w", state.Key, err)
	}
	var record struct {
		Key      string       `json:"key"`
		Programs []rawProgram `json:"programs"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return terminalSearchResult{}, false, fmt.Errorf("decode search cache %s: %w", state.Key, err)
	}
	if record.Key != state.Key || record.Programs == nil {
		return terminalSearchResult{}, false, fmt.Errorf("invalid search cache %s", state.Key)
	}
	return terminalSearchResult{State: state, Programs: record.Programs, Cached: true}, true, nil
}

func writeTerminalSearchCache(result terminalSearchResult) error {
	data, err := json.Marshal(struct {
		Key      string       `json:"key"`
		Programs []rawProgram `json:"programs"`
	}{Key: result.State.Key, Programs: result.Programs})
	if err != nil {
		return err
	}
	path := terminalSearchCachePath(result.State)
	temporary, err := os.CreateTemp(filepath.Dir(path), ".search-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		os.Remove(temporaryPath)
		return err
	}
	if err := temporary.Close(); err != nil {
		os.Remove(temporaryPath)
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		os.Remove(temporaryPath)
		return err
	}
	return nil
}
