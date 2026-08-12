package main

// This file publishes the dependent Postani Student select menus separately
// from programi.json. The service returns menus after each choice, rather than
// a Cartesian product of every filter. Keeping those returned states preserves
// the source behaviour without embedding raw HTML or service responses.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	html "golang.org/x/net/html"
)

const (
	providersAPIURL = postaniStudentBase + "/webservices/Pretraga.svc/izvodjaci"
	placesAPIURL    = postaniStudentBase + "/webservices/Pretraga.svc/MjestoIzvodjenja"
	fieldsAPIURL    = postaniStudentBase + "/webservices/Pretraga.svc/Polja"
)

var studyFilterDefaultLabels = map[string]string{
	"ddlSveuciliste":      "Sva visoka učilišta",
	"ddlVisokoUciliste":   "Sve sastavnice",
	"ddlMjestaIzvodjenja": "Sva mjesta",
	"ddlPodrucje":         "Sva područja",
	"ddlPolja":            "Sva polja",
}

type studyFilterOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type studyFilterMenu struct {
	Options []studyFilterOption `json:"options"`
}

type studyFilterCatalog struct {
	Menus map[string]studyFilterMenu `json:"menus"`
}

type studyFilterTransition struct {
	ParentBranch string            `json:"parent_branch,omitempty"`
	Branch       string            `json:"branch"`
	Selected     studyFilterOption `json:"selected"`
	ResponseRef  string            `json:"response_ref"`
	Sastavnice   []string          `json:"-"`
	Mjesta       []string          `json:"-"`
	Podrucje     string            `json:"-"`
}

type studyFilterControls struct {
	TypeOptions  []studyFilterOption `json:"type_options"`
	QuotaOptions []studyFilterOption `json:"quota_options"`
}

type studyFilterCoverage struct {
	TypeTransitions        int `json:"type_transitions"`
	InstitutionTransitions int `json:"institution_transitions"`
	ComponentTransitions   int `json:"component_transitions"`
	LocationTransitions    int `json:"location_transitions"`
	AreaTransitions        int `json:"area_transitions"`
	ResponseCatalogs       int `json:"response_catalogs"`
	HTTPCalls              int `json:"http_calls"`
}

type studyFilterDocument struct {
	GeneratedAt  string                        `json:"generated_at"`
	Controls     studyFilterControls           `json:"controls"`
	Catalogs     map[string]studyFilterCatalog `json:"catalogs"`
	Types        []studyFilterTransition       `json:"types"`
	Institutions []studyFilterTransition       `json:"institutions"`
	Components   []studyFilterTransition       `json:"components"`
	Locations    []studyFilterTransition       `json:"locations"`
	Areas        []studyFilterTransition       `json:"areas"`
	Coverage     studyFilterCoverage           `json:"coverage"`
}

type studyFilterBranch struct {
	Components  []string
	Locations   []string
	ResponseRef string
}

func shortStudyFilterHash(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])[:20]
}

func studyFilterRequestKey(method, endpoint string, query url.Values, payload map[string]any) string {
	encoded, _ := json.Marshal(map[string]any{"method": method, "endpoint": endpoint, "query": query, "payload": payload})
	return string(encoded)
}

func findStudyFilterSelect(root *html.Node, suffix string) *html.Node {
	return findFirst(root, func(node *html.Node) bool {
		return node.Type == html.ElementNode && node.Data == "select" && strings.HasSuffix(attr(node, "id"), suffix)
	})
}

func studyFilterOptions(selectNode *html.Node) []studyFilterOption {
	var result []studyFilterOption
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "option" {
			result = append(result, studyFilterOption{Value: attr(node, "value"), Label: nodeText(node)})
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(selectNode)
	return result
}

func extractStudyFilterControls(page []byte) (studyFilterControls, error) {
	root, err := html.Parse(strings.NewReader(string(page)))
	if err != nil {
		return studyFilterControls{}, fmt.Errorf("parse study filter page: %w", err)
	}
	typeSelect := findStudyFilterSelect(root, "ddlVrstaNositelja")
	quotaSelect := findStudyFilterSelect(root, "ddlPosebneKvote")
	if typeSelect == nil || quotaSelect == nil {
		return studyFilterControls{}, fmt.Errorf("study filter page is missing required select controls")
	}
	controls := studyFilterControls{TypeOptions: studyFilterOptions(typeSelect), QuotaOptions: studyFilterOptions(quotaSelect)}
	if len(controls.TypeOptions) == 0 || len(controls.QuotaOptions) == 0 {
		return studyFilterControls{}, fmt.Errorf("study filter page has empty required select controls")
	}
	return controls, nil
}

func studyFilterText(value any) string {
	switch item := value.(type) {
	case string:
		return item
	case float64:
		return fmt.Sprintf("%.0f", item)
	case json.Number:
		return item.String()
	default:
		return fmt.Sprint(item)
	}
}

func normalizeStudyFilterCatalog(data map[string]any) studyFilterCatalog {
	mapping := map[string]string{"Nositelji": "ddlSveuciliste", "Izvodjaci": "ddlVisokoUciliste", "MjestaIzvodjenja": "ddlMjestaIzvodjenja", "Podrucja": "ddlPodrucje", "Polja": "ddlPolja"}
	menus := make(map[string]studyFilterMenu, len(mapping))
	for sourceKey, controlKey := range mapping {
		options := []studyFilterOption{{Value: "-1", Label: studyFilterDefaultLabels[controlKey]}}
		if items, ok := data[sourceKey].([]any); ok {
			for _, item := range items {
				if value, ok := item.(map[string]any); ok {
					label := studyFilterText(value["naziv"])
					optionValue := studyFilterText(value["id"])
					if controlKey == "ddlMjestaIzvodjenja" {
						optionValue = label
					}
					if label != "" {
						options = append(options, studyFilterOption{Value: optionValue, Label: label})
					}
				} else if label := studyFilterText(item); label != "" {
					options = append(options, studyFilterOption{Value: label, Label: label})
				}
			}
		}
		menus[controlKey] = studyFilterMenu{Options: options}
	}
	return studyFilterCatalog{Menus: menus}
}

func studyFilterValues(options []studyFilterOption, selected studyFilterOption) []string {
	if selected.Value != "-1" {
		return []string{selected.Value}
	}
	values := make([]string, 0, len(options))
	for _, option := range options {
		values = append(values, option.Value)
	}
	return values
}

func (c *studyHTTPClient) fetchStudyFilterResponse(method, endpoint string, query url.Values, payload map[string]any) (map[string]any, error) {
	requestURL := endpoint
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
	}
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		data, status, requestErr := c.do(method, requestURL, body, ajaxHeaders(programsPageURL))
		if requestErr == nil && status == http.StatusOK {
			var envelope struct {
				D map[string]any `json:"d"`
			}
			if decodeErr := json.Unmarshal(data, &envelope); decodeErr == nil && envelope.D != nil {
				return envelope.D, nil
			} else if decodeErr != nil {
				lastErr = decodeErr
			} else {
				lastErr = fmt.Errorf("empty service envelope")
			}
		} else if requestErr != nil {
			lastErr = requestErr
		} else {
			lastErr = fmt.Errorf("HTTP %d", status)
		}
		time.Sleep(time.Duration(attempt) * 250 * time.Millisecond)
	}
	return nil, lastErr
}

type publishedStudyFilterMenu struct {
	Default studyFilterOption   `json:"default"`
	Options []studyFilterOption `json:"options"`
}

type publishedStudyFilterCatalog struct {
	ID    string                              `json:"id"`
	Menus map[string]publishedStudyFilterMenu `json:"menus"`
}

func publishStudyFilterMenu(menu studyFilterMenu) (publishedStudyFilterMenu, error) {
	if len(menu.Options) == 0 {
		return publishedStudyFilterMenu{}, errors.New("filter menu has no default option")
	}
	options := make([]studyFilterOption, len(menu.Options)-1)
	copy(options, menu.Options[1:])
	return publishedStudyFilterMenu{Default: menu.Options[0], Options: options}, nil
}

func studyFilterPublicDocument(document studyFilterDocument) (map[string]any, error) {
	friendlyKey := map[string]string{
		"ddlVrstaNositelja": "vrsta", "ddlSveuciliste": "nositelj", "ddlVisokoUciliste": "sastavnica",
		"ddlPosebneKvote": "posebna_kvota", "ddlMjestaIzvodjenja": "mjesto", "ddlPodrucje": "podrucje", "ddlPolja": "polje",
	}
	friendlyLabel := map[string]string{
		"vrsta": "Vrsta visokog u?ili?ta", "nositelj": "Visoko u?ili?te / nositelj", "sastavnica": "Sastavnica / izvo?a?",
		"posebna_kvota": "Posebna kvota", "mjesto": "Mjesto izvo?enja", "podrucje": "Podru?je", "polje": "Polje",
	}
	rootRef := ""
	for _, transition := range document.Types {
		if transition.Selected.Value == "-1" {
			rootRef = transition.ResponseRef
			break
		}
	}
	root, ok := document.Catalogs[rootRef]
	if !ok {
		return nil, errors.New("filter discovery has no all-types root state")
	}
	selectors := map[string]any{
		"vrsta":         map[string]any{"label": friendlyLabel["vrsta"], "options": document.Controls.TypeOptions},
		"posebna_kvota": map[string]any{"label": friendlyLabel["posebna_kvota"], "options": document.Controls.QuotaOptions},
	}
	for raw, friendly := range friendlyKey {
		if raw == "ddlVrstaNositelja" || raw == "ddlPosebneKvote" {
			continue
		}
		menu, exists := root.Menus[raw]
		if !exists {
			return nil, fmt.Errorf("root state is missing %s", raw)
		}
		selectors[friendly] = map[string]any{"label": friendlyLabel[friendly], "options": menu.Options}
	}

	catalogRefs := make([]string, 0, len(document.Catalogs))
	for ref := range document.Catalogs {
		catalogRefs = append(catalogRefs, ref)
	}
	sort.Strings(catalogRefs)
	catalogs := make([]publishedStudyFilterCatalog, 0, len(catalogRefs))
	for _, ref := range catalogRefs {
		raw := document.Catalogs[ref]
		menus := map[string]publishedStudyFilterMenu{}
		for rawKey, friendly := range friendlyKey {
			if rawKey == "ddlVrstaNositelja" || rawKey == "ddlPosebneKvote" {
				continue
			}
			menu, exists := raw.Menus[rawKey]
			if !exists {
				return nil, fmt.Errorf("catalog %s is missing %s", ref, rawKey)
			}
			published, err := publishStudyFilterMenu(menu)
			if err != nil {
				return nil, fmt.Errorf("catalog %s %s: %w", ref, rawKey, err)
			}
			menus[friendly] = published
		}
		catalogs = append(catalogs, publishedStudyFilterCatalog{ID: ref, Menus: menus})
	}

	makeTransition := func(transition studyFilterTransition) map[string]any {
		return map[string]any{"select": transition.Selected.Value, "state": transition.ResponseRef}
	}
	typeValues := map[string]string{}
	transitions := map[string]any{"vrsta": []map[string]any{}, "nositelj": []map[string]any{}, "sastavnica": []map[string]any{}, "mjesto": []map[string]any{}, "podrucje": []map[string]any{}, "posebna_kvota": []map[string]any{}}
	for _, transition := range document.Types {
		entry := makeTransition(transition)
		transitions["vrsta"] = append(transitions["vrsta"].([]map[string]any), entry)
		typeValues[transition.Branch] = transition.Selected.Value
	}
	for _, transition := range document.Institutions {
		entry := makeTransition(transition)
		entry["branch"] = transition.Branch
		entry["vrsta"] = typeValues[transition.ParentBranch]
		transitions["nositelj"] = append(transitions["nositelj"].([]map[string]any), entry)
	}
	for _, transition := range document.Components {
		entry := makeTransition(transition)
		entry["branch"] = transition.Branch
		entry["sastavnice"] = transition.Sastavnice
		transitions["sastavnica"] = append(transitions["sastavnica"].([]map[string]any), entry)
		for _, quota := range document.Controls.QuotaOptions {
			quotaEntry := map[string]any{"select": quota.Value, "state": transition.ResponseRef, "sastavnica_branch": transition.Branch}
			transitions["posebna_kvota"] = append(transitions["posebna_kvota"].([]map[string]any), quotaEntry)
		}
	}
	for _, transition := range document.Locations {
		entry := makeTransition(transition)
		entry["branch"] = transition.Branch
		entry["sastavnice"] = transition.Sastavnice
		entry["mjesta"] = transition.Mjesta
		transitions["mjesto"] = append(transitions["mjesto"].([]map[string]any), entry)
	}
	for _, transition := range document.Areas {
		entry := makeTransition(transition)
		entry["branch"] = transition.Branch
		entry["sastavnice"] = transition.Sastavnice
		entry["mjesta"] = transition.Mjesta
		entry["podrucje"] = transition.Podrucje
		transitions["podrucje"] = append(transitions["podrucje"].([]map[string]any), entry)
	}

	return map[string]any{
		"default_state": map[string]string{"vrsta": "-1", "nositelj": "-1", "sastavnica": "-1", "posebna_kvota": "-1", "mjesto": "-1", "podrucje": "-1", "polje": "-1"},
		"selectors":     selectors, "catalogs": catalogs, "transitions": transitions,
		"reset":     map[string]any{"sets": map[string]string{"vrsta": "-1", "nositelj": "-1", "sastavnica": "-1", "mjesto": "-1", "podrucje": "-1", "polje": "-1"}, "keeps": "posebna_kvota"},
		"algorithm": map[string]any{"kind": "finite offline state machine captured from all reachable select changes", "select_change_order": []string{"vrsta", "nositelj", "sastavnica", "posebna_kvota", "mjesto", "podrucje", "polje"}, "effects": map[string][]string{"vrsta": {"nositelj", "sastavnica", "mjesto", "podrucje", "polje"}, "nositelj": {"sastavnica", "mjesto", "podrucje", "polje"}, "sastavnica": {"mjesto", "podrucje", "polje"}, "posebna_kvota": {"mjesto", "podrucje", "polje"}, "mjesto": {"podrucje", "polje"}, "podrucje": {"polje"}, "polje": {}}, "search": "See source/programi.json search_model; all IDs are resolved locally and no HTTP endpoint is used."},
	}, nil
}

func writeStudyFilterDocument(path string, document studyFilterDocument) error {
	published, err := studyFilterPublicDocument(document)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(published)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".study-filters-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	if _, err := temporary.Write(append(encoded, '\n')); err != nil {
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
	info, err := os.Stat(path)
	if err == nil {
		fmt.Printf("Published %s (%d bytes, %d response states)\n", path, info.Size(), len(document.Catalogs))
	}
	return err
}

func refreshStudyFilters(client *studyHTTPClient, sessionHTML []byte, outputPath string) error {
	controls, err := extractStudyFilterControls(sessionHTML)
	if err != nil {
		return err
	}
	document := studyFilterDocument{GeneratedAt: time.Now().UTC().Truncate(time.Second).Format(time.RFC3339), Controls: controls, Catalogs: map[string]studyFilterCatalog{}}
	httpCalls := 0
	responseCache := map[string]map[string]any{}
	fetch := func(method, endpoint string, query url.Values, payload map[string]any) (map[string]any, error) {
		key := studyFilterRequestKey(method, endpoint, query, payload)
		if cached, ok := responseCache[key]; ok {
			return cached, nil
		}
		data, requestErr := client.fetchStudyFilterResponse(method, endpoint, query, payload)
		if requestErr != nil {
			return nil, requestErr
		}
		httpCalls++
		responseCache[key] = data
		return data, nil
	}
	storeCatalog := func(data map[string]any) string {
		ref := shortStudyFilterHash(data)
		if _, exists := document.Catalogs[ref]; !exists {
			document.Catalogs[ref] = normalizeStudyFilterCatalog(data)
		}
		return ref
	}

	typeBranches := make([]studyFilterBranch, 0, len(controls.TypeOptions))
	for index, option := range controls.TypeOptions {
		data, requestErr := fetch(http.MethodGet, componentsURL, url.Values{"id": {option.Value}}, nil)
		if requestErr != nil {
			return fmt.Errorf("filter type %q: %w", option.Label, requestErr)
		}
		responseRef := storeCatalog(data)
		branch := "type:" + shortStudyFilterHash([]string{option.Value, responseRef})
		document.Types = append(document.Types, studyFilterTransition{Branch: branch, Selected: option, ResponseRef: responseRef})
		typeBranches = append(typeBranches, studyFilterBranch{ResponseRef: responseRef})
		studyProgress("filters", index+1, len(controls.TypeOptions), " - types")
	}

	institutionBranches := []studyFilterBranch{}
	for typeIndex, typeTransition := range document.Types {
		catalog := document.Catalogs[typeTransition.ResponseRef]
		for _, option := range catalog.Menus["ddlSveuciliste"].Options {
			payload := map[string]any{"lista": studyFilterValues(catalog.Menus["ddlSveuciliste"].Options, option), "idPodrucja": -1, "mjesta": []string{""}}
			data, requestErr := fetch(http.MethodPost, providersAPIURL, nil, payload)
			if requestErr != nil {
				return fmt.Errorf("filter institution %q: %w", option.Label, requestErr)
			}
			responseRef := storeCatalog(data)
			branch := "institution:" + shortStudyFilterHash([]any{typeTransition.Branch, option.Value, responseRef})
			document.Institutions = append(document.Institutions, studyFilterTransition{ParentBranch: typeTransition.Branch, Branch: branch, Selected: option, ResponseRef: responseRef})
			institutionBranches = append(institutionBranches, studyFilterBranch{ResponseRef: responseRef})
		}
		studyProgress("filters", typeIndex+1, len(document.Types), " - institutions")
	}

	componentBranches := []studyFilterBranch{}
	for index, transition := range document.Institutions {
		catalog := document.Catalogs[transition.ResponseRef]
		for _, option := range catalog.Menus["ddlVisokoUciliste"].Options {
			components := studyFilterValues(catalog.Menus["ddlVisokoUciliste"].Options, option)
			payload := map[string]any{"lista": components, "idPodrucja": -1, "mjesta": []string{""}}
			data, requestErr := fetch(http.MethodPost, placesAPIURL, nil, payload)
			if requestErr != nil {
				return fmt.Errorf("filter component %q: %w", option.Label, requestErr)
			}
			responseRef := storeCatalog(data)
			branch := "component:" + shortStudyFilterHash([]any{transition.Branch, option.Value, responseRef})
			document.Components = append(document.Components, studyFilterTransition{ParentBranch: transition.Branch, Branch: branch, Selected: option, ResponseRef: responseRef, Sastavnice: components})
			componentBranches = append(componentBranches, studyFilterBranch{Components: components, ResponseRef: responseRef})
		}
		studyProgress("filters", index+1, len(document.Institutions), " - components")
	}

	locationBranches := []studyFilterBranch{}
	for index, transition := range document.Components {
		catalog := document.Catalogs[transition.ResponseRef]
		components := componentBranches[index].Components
		for _, option := range catalog.Menus["ddlMjestaIzvodjenja"].Options {
			locations := studyFilterValues(catalog.Menus["ddlMjestaIzvodjenja"].Options, option)
			payload := map[string]any{"lista": components, "mjesta": locations, "idPodrucja": -1}
			data, requestErr := fetch(http.MethodPost, fieldsAPIURL, nil, payload)
			if requestErr != nil {
				return fmt.Errorf("filter location %q: %w", option.Label, requestErr)
			}
			responseRef := storeCatalog(data)
			branch := "location:" + shortStudyFilterHash([]any{transition.Branch, option.Value, responseRef})
			document.Locations = append(document.Locations, studyFilterTransition{ParentBranch: transition.Branch, Branch: branch, Selected: option, ResponseRef: responseRef, Sastavnice: components, Mjesta: locations})
			locationBranches = append(locationBranches, studyFilterBranch{Components: components, Locations: locations, ResponseRef: responseRef})
		}
		studyProgress("filters", index+1, len(document.Components), " - locations")
	}

	for index, transition := range document.Locations {
		catalog := document.Catalogs[transition.ResponseRef]
		branch := locationBranches[index]
		for _, option := range catalog.Menus["ddlPodrucje"].Options {
			payload := map[string]any{"lista": branch.Components, "mjesta": branch.Locations, "idPodrucja": option.Value}
			data, requestErr := fetch(http.MethodPost, fieldsAPIURL, nil, payload)
			if requestErr != nil {
				return fmt.Errorf("filter area %q: %w", option.Label, requestErr)
			}
			responseRef := storeCatalog(data)
			areaBranch := "area:" + shortStudyFilterHash([]any{transition.Branch, option.Value, responseRef})
			document.Areas = append(document.Areas, studyFilterTransition{ParentBranch: transition.Branch, Branch: areaBranch, Selected: option, ResponseRef: responseRef, Sastavnice: branch.Components, Mjesta: branch.Locations, Podrucje: option.Value})
		}
		studyProgress("filters", index+1, len(document.Locations), " - areas")
	}

	// Keep output deterministic even when the upstream list order happens to change.
	sort.Slice(document.Types, func(i, j int) bool { return document.Types[i].Selected.Label < document.Types[j].Selected.Label })
	document.Coverage = studyFilterCoverage{TypeTransitions: len(document.Types), InstitutionTransitions: len(document.Institutions), ComponentTransitions: len(document.Components), LocationTransitions: len(document.Locations), AreaTransitions: len(document.Areas), ResponseCatalogs: len(document.Catalogs), HTTPCalls: httpCalls}
	return writeStudyFilterDocument(outputPath, document)
}
