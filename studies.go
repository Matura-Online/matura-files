package main

// The study refresh is intentionally kept in this file and called from main.go.
// It mirrors the original scrape.py -> fetch_details.py -> parse_details.py
// sequence, but keeps all HTML files in one temporary directory and publishes
// only source/programi.json.

import (
	"archive/zip"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	html "golang.org/x/net/html"
)

const (
	postaniStudentBase = "https://www.postani-student.hr"
	programsPageURL    = postaniStudentBase + "/Ucilista/Nositelji.aspx"
	componentsURL      = postaniStudentBase + "/webservices/Pretraga.svc/nositelji"
	programsAPIURL     = postaniStudentBase + "/webservices/Pretraga.svc/PretraziPrograme"
	detailsURL         = postaniStudentBase + "/usercontrols/uvjeticontainer.aspx"
)

var (
	percentPattern   = regexp.MustCompile(`(-?\d+(?:[.,]\d+)?)\s*%`)
	programPattern   = regexp.MustCompile(`(?s)<strong>(.+?)</strong><br/>(.+?) \((\d+) bodova, ([\d.]+) godin[ae]?, (.+?)\)\s*$`)
	qualifierPattern = regexp.MustCompile(`(?is)^(.*?);\s*(smjer(?:ovi)?|modul(?:i)?)\s*[:;]\s*(.*)$`)
	markerPattern    = regexp.MustCompile(`\*+`)
)

var numberWords = map[string]int{
	"jedan": 1, "jednog": 1, "jednoga": 1, "jednu": 1, "jedno": 1,
	"dva": 2, "dvije": 2, "tri": 3, "četiri": 4, "cetiri": 4,
	"pet": 5, "šest": 6, "sest": 6, "sedam": 7, "osam": 8,
	"devet": 9, "deset": 10, "jedanaest": 11, "dvanaest": 12,
	"trinaest": 13, "četrnaest": 14, "cetrnaest": 14, "petnaest": 15,
	"šesnaest": 16, "sesnaest": 16, "sedamnaest": 17, "osamnaest": 18,
	"devetnaest": 19, "dvadeset": 20,
}

var studyTypeEnums = map[string]int{
	"redovni prijediplomski sveučilišni studij":                1,
	"izvanredni prijediplomski sveučilišni studij":             2,
	"redovni prijediplomski stručni studij":                    3,
	"izvanredni prijediplomski stručni studij":                 4,
	"redovni integrirani prijediplomski i diplomski studij":    5,
	"izvanredni integrirani prijediplomski i diplomski studij": 6,
	"redovni kratki stručni studij":                            7,
	"izvanredni kratki stručni studij":                         8,
}

func studyProgress(label string, current, total int, suffix string) {
	if total < 1 {
		return
	}
	const width = 40
	filled := width * current / total
	bar := strings.Repeat("=", filled) + strings.Repeat("-", width-filled)
	percent := float64(current) / float64(total) * 100
	fmt.Printf("\r   %-8s [%s] %d/%d (%3.0f%%)%s", label, bar, current, total, percent, suffix)
	if current >= total {
		fmt.Println()
	}
}

var nameFixes = map[string]string{
	"Proizvodno stojarstvo":        "Proizvodno strojarstvo",
	"Fizioterapija (oline studij)": "Fizioterapija (online studij)",
}

type studyHTTPClient struct {
	client  *http.Client
	headers http.Header
}

type rawProgram struct {
	ID         int    `json:"id"`
	IDPrograma int    `json:"idPrograma"`
	Naziv      string `json:"naziv"`
	Nositelj   string `json:"nositelj"`
	Izvodjac   string `json:"izvodjac"`
	Mjesto     string `json:"mjesto"`
	Programi   string `json:"programi"`
}

type programMeta struct {
	ID           int                 `json:"id"`
	IDPrograma   int                 `json:"idPrograma"`
	Naziv        string              `json:"naziv"`
	Smjer        []string            `json:"smjer"`
	Modul        []string            `json:"modul"`
	Nositelj     string              `json:"nositelj"`
	Izvodjac     string              `json:"izvodjac"`
	Mjesto       string              `json:"mjesto"`
	ECTS         int                 `json:"ects"`
	TrajanjeGod  float64             `json:"trajanje_god"`
	VrstaStudija *int                `json:"vrsta_studija"`
	Pretraga     studySearchRelation `json:"pretraga"`
}

// studySearchRelation is the captured membership used by the official search
// endpoint. It is deliberately separate from display fields: a program can be
// listed under several areas/fields while still having one displayed place and
// quota partition. Refreshing detail HTML must never invent those memberships.
type studySearchRelation struct {
	SastavnicaID string   `json:"sastavnica_id"`
	Podrucja     []string `json:"podrucja"`
	Polja        []string `json:"polja"`
	PosebnaKvota string   `json:"posebna_kvota"`
	Redoslijed   int      `json:"redoslijed"`
}

func (relation studySearchRelation) valid() bool {
	return relation.SastavnicaID != "" && len(relation.Podrucja) > 0 && len(relation.Polja) > 0 && relation.PosebnaKvota != ""
}

type tableRow struct {
	Index int      `json:"index"`
	Cells []string `json:"cells"`
}

type tableSnapshot struct {
	TableID string     `json:"table_id"`
	Caption *string    `json:"caption"`
	Header  []string   `json:"header"`
	Rows    []tableRow `json:"rows"`
}

type noteBlock struct {
	Marker *string `json:"marker"`
	Text   string  `json:"text"`
}

type noteDocument struct {
	RawText *string
	Blocks  []noteBlock
}

var tableIDs = map[string]string{
	"preduvjeti":         "ucUvjeti_gdvDodatneVjestine",
	"ocjene":             "ucUvjeti_gdvOcjeneIzSrednjeSkole",
	"obvezni":            "ucUvjeti_gdvObvezniUvjeti",
	"izborni":            "ucUvjeti_gdvIzborniDio",
	"dodatne_provjere":   "ucUvjeti_gdvDodatniUvjeti",
	"natjecanja":         "ucUvjeti_gdvVrednovanjaNatjecanja",
	"sportasi":           "ucUvjeti_gdvVrednovanjaSportasa",
	"ocjene_mature":      "ucUvjeti_gdvVrednovanjeOcjenaUGrupi",
	"posebna_postignuca": "ucUvjeti_gdvDrugaPosebnaPostignuca",
}

var noteIDs = map[string]string{
	"obvezni":            "ucUvjeti_LblNapomenaObavezni",
	"izborni":            "ucUvjeti_LblNapomena",
	"natjecanja":         "ucUvjeti_lblNapomenaatjecanja",
	"posebna_postignuca": "ucUvjeti_lblPosebnaPostignucaNapomena",
}

func newStudyHTTPClient() (*studyHTTPClient, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &studyHTTPClient{
		client: &http.Client{
			Jar:     jar,
			Timeout: 45 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // The source has historically used an untrusted certificate.
			},
		},
		headers: http.Header{
			"User-Agent": []string{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/150.0 Safari/537.36"},
			"DNT":        []string{"1"},
		},
	}, nil
}

func (c *studyHTTPClient) do(method, url string, body []byte, extra http.Header) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return nil, 0, err
	}
	for key, values := range c.headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	for key, values := range extra {
		req.Header.Del(key)
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(resp.Body)
	return data, resp.StatusCode, readErr
}

func (c *studyHTTPClient) loadSession() ([]byte, error) {
	data, status, err := c.do(http.MethodGet, programsPageURL, nil, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("study page returned HTTP %d", status)
	}
	fmt.Printf("Study source session loaded (%d bytes)\n", len(data))
	return data, nil
}

func ajaxHeaders(referer string) http.Header {
	return http.Header{
		"Accept":           []string{"application/json, text/javascript, */*; q=0.01"},
		"Content-Type":     []string{"application/json; charset=UTF-8"},
		"X-Requested-With": []string{"XMLHttpRequest"},
		"Referer":          []string{referer},
	}
}

func (c *studyHTTPClient) fetchCatalog(relations map[int]studySearchRelation) ([]programMeta, error) {
	data, status, err := c.do(http.MethodGet, componentsURL+"?id=-1", nil, ajaxHeaders(programsPageURL))
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("components API returned HTTP %d", status)
	}
	var componentEnvelope struct {
		D struct {
			Izvodjaci []struct {
				ID int `json:"id"`
			} `json:"Izvodjaci"`
		} `json:"d"`
	}
	if err := json.Unmarshal(data, &componentEnvelope); err != nil {
		return nil, fmt.Errorf("decode components response: %w", err)
	}
	ids := []string{"-1"}
	for _, item := range componentEnvelope.D.Izvodjaci {
		ids = append(ids, strconv.Itoa(item.ID))
	}
	fmt.Printf("Fetched %d study components\n", len(ids)-1)

	all := map[int]rawProgram{}
	// -1 is regular admission only. The special-quota partitions are disjoint
	// result sets, so all three must be fetched to publish the full catalog.
	for _, quota := range []string{"-1", "1", "2"} {
		totalPages := 0
		for page := 1; page <= 200; page++ {
			payload := map[string]any{
				"lista": ids, "search": "", "searchVisokaUcilista": "", "podrucje": "-1",
				"polje": "-1", "Mjesto": "Sva mjesta", "usporedba": true,
				"posebnaKvota": quota, "page": page,
			}
			body, err := json.Marshal(payload)
			if err != nil {
				return nil, err
			}
			data, status, err = c.do(http.MethodPost, programsAPIURL, body, ajaxHeaders(programsPageURL))
			if err != nil {
				return nil, fmt.Errorf("fetch catalog quota %s page %d: %w", quota, page, err)
			}
			if status != http.StatusOK {
				return nil, fmt.Errorf("catalog quota %s page %d returned HTTP %d", quota, page, status)
			}
			var envelope struct {
				D struct {
					TotalPages int          `json:"TotalPages"`
					Programi   []rawProgram `json:"Programi"`
				} `json:"d"`
			}
			if err := json.Unmarshal(data, &envelope); err != nil {
				return nil, fmt.Errorf("decode catalog quota %s page %d: %w", quota, page, err)
			}
			if totalPages == 0 {
				totalPages = envelope.D.TotalPages
				if totalPages < 1 {
					return nil, fmt.Errorf("catalog quota %s returned no pages", quota)
				}
				fmt.Printf("Catalog quota %s has %d pages\n", quota, totalPages)
			}
			for _, item := range envelope.D.Programi {
				if previous, exists := all[item.IDPrograma]; exists && previous.ID != item.ID {
					return nil, fmt.Errorf("catalog program %d appears with conflicting ids %d and %d", item.IDPrograma, previous.ID, item.ID)
				}
				all[item.IDPrograma] = item
			}
			studyProgress("catalog", page, totalPages, fmt.Sprintf(" - quota %s, %d programs", quota, len(all)))
			if page >= totalPages {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
	}

	parsed := make([]programMeta, 0, len(all))
	for _, item := range all {
		meta, err := parseCatalogProgram(item)
		if err != nil {
			return nil, fmt.Errorf("catalog program %d: %w", item.IDPrograma, err)
		}
		relation, ok := relations[meta.IDPrograma]
		if !ok || !relation.valid() {
			return nil, fmt.Errorf("catalog program %d has no verified search relation; capture the new state graph before publishing", meta.IDPrograma)
		}
		meta.Pretraga = relation
		parsed = append(parsed, meta)
	}
	if len(parsed) == 0 {
		return nil, errors.New("catalog API returned no programs")
	}
	sort.Slice(parsed, func(i, j int) bool { return parsed[i].IDPrograma < parsed[j].IDPrograma })
	return parsed, nil
}

func parseCatalogProgram(item rawProgram) (programMeta, error) {
	match := programPattern.FindStringSubmatch(strings.TrimSpace(item.Programi))
	if len(match) == 0 {
		return programMeta{}, fmt.Errorf("cannot parse program summary %q", item.Programi)
	}
	e, err := strconv.Atoi(match[3])
	if err != nil {
		return programMeta{}, fmt.Errorf("invalid ECTS: %w", err)
	}
	duration, err := strconv.ParseFloat(match[4], 64)
	if err != nil {
		return programMeta{}, fmt.Errorf("invalid duration: %w", err)
	}
	fullName := nameFixes[strings.TrimSpace(match[2])]
	if fullName == "" {
		fullName = strings.TrimSpace(match[2])
	}
	name, directions, modules := splitQualifier(fullName)
	provider := item.Izvodjac
	if provider == item.Nositelj {
		provider = ""
	}
	var studyType *int
	if value, ok := studyTypeEnums[strings.ToLower(strings.TrimSpace(match[5]))]; ok {
		studyType = &value
	}
	return programMeta{
		ID: item.ID, IDPrograma: item.IDPrograma, Naziv: name, Smjer: directions,
		Modul: modules, Nositelj: item.Nositelj, Izvodjac: provider, Mjesto: item.Mjesto,
		ECTS: e, TrajanjeGod: duration, VrstaStudija: studyType,
	}, nil
}

func splitQualifier(value string) (string, []string, []string) {
	match := qualifierPattern.FindStringSubmatch(value)
	if len(match) == 0 {
		return strings.TrimSpace(value), []string{}, []string{}
	}
	parts := strings.Split(match[3], ";")
	var values []string
	for _, piece := range parts {
		for _, commaPiece := range strings.Split(piece, ",") {
			if value := strings.TrimSpace(commaPiece); value != "" {
				values = append(values, value)
			}
		}
	}
	if strings.HasPrefix(strings.ToLower(match[2]), "smjer") {
		return strings.TrimSpace(match[1]), values, []string{}
	}
	return strings.TrimSpace(match[1]), []string{}, values
}

func (c *studyHTTPClient) fetchDetail(id int) ([]byte, error) {
	url := detailsURL + "?id=" + strconv.Itoa(id)
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		data, status, err := c.do(http.MethodGet, url, nil, nil)
		if err == nil && status == http.StatusOK && len(data) > 2000 {
			return data, nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("HTTP %d with %d bytes", status, len(data))
		}
		fmt.Printf("detail %d attempt %d failed: %v\n", id, attempt, lastErr)
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	return nil, lastErr
}

func refreshStudyPrograms(outputPath, filtersOutputPath, htmlArchivePath string) error {
	client, err := newStudyHTTPClient()
	if err != nil {
		return err
	}
	temporaryDir, err := os.MkdirTemp("", "matura-programi-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporaryDir)
	sessionHTML, err := client.loadSession()
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(temporaryDir, "catalog.html"), sessionHTML, 0o600); err != nil {
		return fmt.Errorf("save study catalog HTML: %w", err)
	}
	if err := refreshStudyFilters(client, sessionHTML, filtersOutputPath); err != nil {
		return fmt.Errorf("refresh dependent study filters: %w", err)
	}
	relations, err := captureStudySearchRelations(client, filtersOutputPath)
	if err != nil {
		return fmt.Errorf("capture program search relations: %w", err)
	}
	catalog, err := client.fetchCatalog(relations)
	if err != nil {
		return err
	}
	fmt.Printf("Refreshing %d study detail pages\n", len(catalog))

	details := make([]map[string]any, 0, len(catalog))
	for index, meta := range catalog {
		data, err := client.fetchDetail(meta.IDPrograma)
		if err != nil {
			return fmt.Errorf("detail %d/%d (%d): %w", index+1, len(catalog), meta.IDPrograma, err)
		}
		path := filepath.Join(temporaryDir, strconv.Itoa(meta.IDPrograma)+".html")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return err
		}
		detail, err := parseStudyDetail(path, meta)
		if err != nil {
			return fmt.Errorf("parse detail %d: %w", meta.IDPrograma, err)
		}
		record := catalogRecord(meta)
		record["detalji"] = detail
		details = append(details, record)
		studyProgress("details", index+1, len(catalog), fmt.Sprintf(" - %d ok", meta.IDPrograma))
		time.Sleep(200 * time.Millisecond)
	}

	if htmlArchivePath != "" {
		if err := zipStudyHTMLDirectory(temporaryDir, htmlArchivePath); err != nil {
			return fmt.Errorf("archive fetched study HTML: %w", err)
		}
	}
	return writeStudyCatalog(outputPath, details)
}

// zipStudyHTMLDirectory stores the fetched source pages as a single audit
// artifact. The archive contains catalog.html plus numeric detail filenames,
// never the temporary absolute path used by the runner.
func zipStudyHTMLDirectory(htmlDir, archivePath string) error {
	entries, err := os.ReadDir(htmlDir)
	if err != nil {
		return fmt.Errorf("read fetched HTML directory: %w", err)
	}
	if len(entries) == 0 {
		return errors.New("fetched HTML directory is empty")
	}
	if directory := filepath.Dir(archivePath); directory != "." {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return fmt.Errorf("create HTML archive directory: %w", err)
		}
	}
	temporary, err := os.CreateTemp(filepath.Dir(archivePath), ".programi-html-*.zip")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	archive := zip.NewWriter(temporary)
	archivedCount := 0
	for _, entry := range entries {
		if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".html" {
			continue
		}
		input, err := os.Open(filepath.Join(htmlDir, entry.Name()))
		if err != nil {
			archive.Close()
			temporary.Close()
			return fmt.Errorf("open %s: %w", entry.Name(), err)
		}
		header := &zip.FileHeader{Name: filepath.Base(entry.Name()), Method: zip.Deflate}
		header.SetModTime(time.Unix(0, 0).UTC())
		writer, err := archive.CreateHeader(header)
		if err == nil {
			_, err = io.Copy(writer, input)
		}
		closeErr := input.Close()
		if err != nil {
			archive.Close()
			temporary.Close()
			return fmt.Errorf("archive %s: %w", entry.Name(), err)
		}
		if closeErr != nil {
			archive.Close()
			temporary.Close()
			return fmt.Errorf("close %s: %w", entry.Name(), closeErr)
		}
		archivedCount++
	}
	if archivedCount == 0 {
		archive.Close()
		temporary.Close()
		return errors.New("fetched HTML directory contains no HTML files")
	}
	if err := archive.Close(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, archivePath); err != nil {
		return fmt.Errorf("publish HTML archive %s: %w", archivePath, err)
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		return err
	}
	fmt.Printf("Archived fetched study HTML: %s (%d bytes)\n", archivePath, info.Size())
	return nil
}

func parseStudyProgramsFromHTML(htmlDir, catalogPath, outputPath string) error {
	catalog, err := loadStudyCatalog(catalogPath)
	if err != nil {
		return err
	}
	if err := validateStudyHTMLDirectory(htmlDir, catalog); err != nil {
		return err
	}
	fmt.Printf("Parsing %d existing study HTML files from %s\n", len(catalog), htmlDir)
	details := make([]map[string]any, 0, len(catalog))
	for index, meta := range catalog {
		path := filepath.Join(htmlDir, strconv.Itoa(meta.IDPrograma)+".html")
		detail, err := parseStudyDetail(path, meta)
		if err != nil {
			return fmt.Errorf("parse detail %d/%d (%d): %w", index+1, len(catalog), meta.IDPrograma, err)
		}
		record := catalogRecord(meta)
		record["detalji"] = detail
		details = append(details, record)
		studyProgress("details", index+1, len(catalog), fmt.Sprintf(" - %d ok", meta.IDPrograma))
	}
	return writeStudyCatalog(outputPath, details)
}

func loadStudyCatalog(path string) ([]programMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read study catalog %s: %w", path, err)
	}
	trimmed := bytes.TrimSpace(data)
	var catalog []programMeta
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &catalog); err != nil {
			return nil, fmt.Errorf("decode study catalog %s: %w", path, err)
		}
	} else {
		var envelope struct {
			Programi []programMeta `json:"programi"`
		}
		if err := json.Unmarshal(trimmed, &envelope); err != nil {
			return nil, fmt.Errorf("decode study catalog %s: %w", path, err)
		}
		catalog = envelope.Programi
	}
	if len(catalog) == 0 {
		return nil, fmt.Errorf("study catalog %s contains no programs", path)
	}
	sort.Slice(catalog, func(i, j int) bool { return catalog[i].IDPrograma < catalog[j].IDPrograma })
	for index, meta := range catalog {
		if meta.IDPrograma == 0 {
			return nil, fmt.Errorf("study catalog row %d has no idPrograma", index+1)
		}
		if index > 0 && catalog[index-1].IDPrograma == meta.IDPrograma {
			return nil, fmt.Errorf("study catalog contains duplicate idPrograma %d", meta.IDPrograma)
		}
	}
	return catalog, nil
}

func validateStudyHTMLDirectory(htmlDir string, catalog []programMeta) error {
	entries, err := os.ReadDir(htmlDir)
	if err != nil {
		return fmt.Errorf("read study HTML directory %s: %w", htmlDir, err)
	}
	ids := map[int]bool{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".html" {
			continue
		}
		id, err := strconv.Atoi(strings.TrimSuffix(entry.Name(), ".html"))
		if err != nil {
			return fmt.Errorf("study HTML filename %q is not numeric", entry.Name())
		}
		ids[id] = true
	}
	if len(ids) != len(catalog) {
		return fmt.Errorf("study HTML/catalog count mismatch: %d HTML files, %d catalog programs", len(ids), len(catalog))
	}
	for _, meta := range catalog {
		if !ids[meta.IDPrograma] {
			return fmt.Errorf("catalog program %d has no matching HTML file", meta.IDPrograma)
		}
	}
	return nil
}

func writeStudyCatalog(outputPath string, details []map[string]any) error {
	generated := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
	payload := map[string]any{
		"generated_at":     generated,
		"refresh_schedule": []string{"01-01", "01-03", "01-06", "01-10"},
		"programi":         details,
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return err
	}
	// Keep the published artifact compact. Temporary HTML is never embedded;
	// compact JSON also avoids adding many megabytes of indentation to a file
	// that is downloaded by every client.
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if err := validateStudyCatalogJSON(encoded); err != nil {
		return fmt.Errorf("generated study catalog does not match its schema: %w", err)
	}
	temporaryOutput, err := os.CreateTemp(filepath.Dir(outputPath), ".programi-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporaryOutput.Name()
	_ = temporaryOutput.Close()
	defer os.Remove(temporaryPath)
	if err := os.WriteFile(temporaryPath, append(encoded, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, outputPath); err != nil {
		return err
	}
	fmt.Printf("Published %s (%d programs, generated_at %s)\n", outputPath, len(details), generated)
	return nil
}

func catalogRecord(meta programMeta) map[string]any {
	return map[string]any{
		"id": meta.ID, "idPrograma": meta.IDPrograma, "naziv": meta.Naziv,
		"smjer": meta.Smjer, "modul": meta.Modul, "nositelj": meta.Nositelj,
		"izvodjac": meta.Izvodjac, "mjesto": meta.Mjesto, "ects": meta.ECTS,
		"trajanje_god": meta.TrajanjeGod, "vrsta_studija": meta.VrstaStudija,
		"pretraga": meta.Pretraga,
	}
}

func cleanText(value string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(value, "\u00a0", " ")), " ")
}

func nodeText(node *html.Node) string {
	if node == nil {
		return ""
	}
	var builder strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			builder.WriteString(current.Data)
			return
		}
		if current.Type == html.ElementNode && current.Data == "br" {
			builder.WriteByte(' ')
			return
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return cleanText(builder.String())
}

func nodeTextLines(node *html.Node) string {
	if node == nil {
		return ""
	}
	var builder strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			builder.WriteString(current.Data)
			return
		}
		if current.Type == html.ElementNode && (current.Data == "br" || current.Data == "p" || current.Data == "div" || current.Data == "li") {
			builder.WriteByte('\n')
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
		if current.Type == html.ElementNode && (current.Data == "p" || current.Data == "div" || current.Data == "li") {
			builder.WriteByte('\n')
		}
	}
	walk(node)
	return builder.String()
}

func findByID(root *html.Node, id string) *html.Node {
	var result *html.Node
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if result != nil || node == nil {
			return
		}
		if node.Type == html.ElementNode && attr(node, "id") == id {
			result = node
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return result
}

func findFirst(root *html.Node, predicate func(*html.Node) bool) *html.Node {
	var result *html.Node
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if result != nil || node == nil {
			return
		}
		if predicate(node) {
			result = node
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return result
}

func attr(node *html.Node, name string) string {
	for _, item := range node.Attr {
		if item.Key == name {
			return item.Val
		}
	}
	return ""
}

func directChildren(node *html.Node, names ...string) []*html.Node {
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[name] = true
	}
	var result []*html.Node
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && wanted[child.Data] {
			result = append(result, child)
		}
	}
	return result
}

func parentElement(node *html.Node) *html.Node {
	if node == nil || node.Parent == nil || node.Parent.Type != html.ElementNode {
		return nil
	}
	return node.Parent
}

func parseStudyDetail(path string, meta programMeta) (map[string]any, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	document, err := html.Parse(file)
	if err != nil {
		return nil, err
	}
	basic, _, err := parseHeader(document)
	if err != nil {
		return nil, err
	}
	snapshots := map[string]tableSnapshot{}
	for section := range tableIDs {
		snapshots[section] = tableSnapshotFor(document, section)
	}
	notes := map[string]noteDocument{}
	for section := range noteIDs {
		notes[section] = noteDocumentFor(document, section)
	}

	preconditions := parsePreconditions(snapshots["preduvjeti"])
	grades, err := parseRows(snapshots["ocjene"], "ocjene")
	if err != nil {
		return nil, err
	}
	mandatory, err := parseRows(snapshots["obvezni"], "obvezni")
	if err != nil {
		return nil, err
	}
	elective, err := parseElective(snapshots["izborni"], notes["izborni"])
	if err != nil {
		return nil, err
	}
	additional, additionalGroups, err := parseAdditional(snapshots["dodatne_provjere"])
	if err != nil {
		return nil, err
	}
	competitions, err := parseRows(snapshots["natjecanja"], "natjecanja")
	if err != nil {
		return nil, err
	}
	athletes, err := parseRows(snapshots["sportasi"], "sportasi")
	if err != nil {
		return nil, err
	}
	achievements, err := parseRows(snapshots["posebna_postignuca"], "posebna_postignuca")
	if err != nil {
		return nil, err
	}
	maturity, err := parseRows(snapshots["ocjene_mature"], "ocjene_mature")
	if err != nil {
		return nil, err
	}
	mandatoryRules := inferNotes(notes["obvezni"], "obvezni")
	competitionRules := inferNotes(notes["natjecanja"], "natjecanja")
	achievementRules := inferNotes(notes["posebna_postignuca"], "posebna_postignuca")
	mandatoryRaw := notes["obvezni"].RawText
	mandatoryText := stringValue(mandatoryRaw)
	flags := map[string]any{
		"eu_izuzece":        regexp.MustCompile(`(?i)državljanima zemalja članica EU.*prvi jezik`).MatchString(mandatoryText),
		"priznaje_b_razinu": regexp.MustCompile(`(?i)osnovne\s*\(B\)\s*razine`).MatchString(mandatoryText),
		"priznaje_a_razinu": regexp.MustCompile(`(?i)više\s*\(A\)\s*razine`).MatchString(mandatoryText),
		"obvezno_za_sve":    len(mandatoryRules) > 0 && anyRule(mandatoryRules, "obvezni_dio_obvezan_za_sve"),
	}
	var thresholdRules []map[string]any
	for _, rule := range mandatoryRules {
		if boolValue(rule["prag_izuzece_prije_2010_ili_izvan_rh"]) || boolValue(rule["zamjena_dodatne_provjere"]) {
			thresholdRules = append(thresholdRules, rule)
		}
	}
	var threshold any
	if len(thresholdRules) == 1 {
		threshold = thresholdRules[0]
	} else if len(thresholdRules) > 1 {
		threshold = thresholdRules
	}
	detail := map[string]any{
		"idPrograma": meta.IDPrograma,
		"osnovno":    basic, "kvota": parseQuota(document),
		"preduvjeti": preconditions,
		"ocjene":     grades, "hrvatski_jezik": flags, "prag": threshold,
		"obvezni": mandatory, "obvezni_pravila": nilIfEmpty(mandatoryRules),
		"izborni": elective, "dodatne_vjestine": additional,
		"dodatne_provjere_grupe": additionalGroups, "natjecanja": competitions,
		"natjecanja_pravila": nilIfEmpty(competitionRules), "sportasi": athletes,
		"druga_posebna_postignuca":          achievements,
		"posebna_postignuca_pravila":        nilIfEmpty(achievementRules),
		"vrednovanje_ocjena_mature":         maturity,
		"vrednovanje_ocjena_mature_pravila": nilIfEmpty(groupRules(maturity, func(row map[string]any) string { return stringValue(row["ispit"]) })),
	}
	crossRules := append(groupRules(maturity, func(row map[string]any) string { return stringValue(row["ispit"]) }), groupRules(competitions, func(row map[string]any) string {
		return strings.Join([]string{stringValue(row["kategorija"]), stringValue(row["disciplina"]), stringValue(row["razred_od"]), stringValue(row["razred_do"])}, "|")
	})...)
	for _, rule := range achievementRules {
		if maximum, ok := rule["maksimalno_pct"]; ok {
			ruleCopy := cloneMap(rule)
			ruleCopy["sekcije"] = []string{"natjecanja", "sportasi", "druga_posebna_postignuca"}
			ruleCopy["zajednicki_maksimalno_pct"] = maximum
			ruleCopy["ne_zbrajati_duplikat"] = true
			crossRules = append(crossRules, ruleCopy)
		}
	}
	detail["medusekcijska_pravila"] = crossRules
	limitations := auditDetail(detail, notes)
	detail["ogranicenja_izvora"] = limitations
	status := "spremno"
	if len(limitations) > 0 {
		status = "spremno_uz_ogranicenja_izvora"
	}
	detail["kalkulator_spremnost"] = map[string]any{"status": status, "zahtijeva_rucni_pregled": len(limitations) > 0, "razlozi": limitationTypes(limitations)}
	return detail, nil
}

func parseHeader(document *html.Node) (map[string]any, string, error) {
	panel := findByID(document, "ucUvjeti_pnlOsnovniPodaci")
	cell := parentElement(panel)
	if panel == nil || cell == nil {
		return nil, "", errors.New("header structure: basic-data panel missing")
	}
	h1 := directChildren(cell, "h1")
	h2 := directChildren(cell, "h2")
	if len(h1) == 0 || len(h2) < 2 {
		return nil, "", errors.New("header structure: expected h1 and two h2 elements")
	}
	originalName := nodeText(h2[len(h2)-1])
	originalProvider := nodeText(h2[0])
	provider := any(nil)
	if originalProvider != "" && originalProvider != nodeText(h1[0]) {
		provider = originalProvider
	}
	var paragraphs []*html.Node
	for sibling := h2[0].NextSibling; sibling != nil && sibling != h2[len(h2)-1]; sibling = sibling.NextSibling {
		if sibling.Type == html.ElementNode && sibling.Data == "p" {
			paragraphs = append(paragraphs, sibling)
		}
	}
	var address, phone, email any
	for _, paragraph := range paragraphs {
		// The original BeautifulSoup parser did not insert a separator for the
		// source's uppercase `</BR>` tags in this header block. Preserve that
		// quirk for exact compatibility with the initial custom export.
		value := nodeTextNoBreak(paragraph)
		bold := findFirst(paragraph, func(node *html.Node) bool { return node.Type == html.ElementNode && node.Data == "b" })
		label := strings.TrimSuffix(strings.ToLower(nodeText(bold)), ":")
		switch label {
		case "telefon":
			v := regexp.MustCompile(`(?i)^Telefon\s*:\s*`).ReplaceAllString(value, "")
			if v != "" {
				phone = v
			}
		case "e-mail", "email":
			v := regexp.MustCompile(`(?i)^E-?Mail\s*:\s*`).ReplaceAllString(value, "")
			if v != "" {
				email = v
			}
		default:
			if findFirst(paragraph, func(node *html.Node) bool { return node.Type == html.ElementNode && node.Data == "a" }) == nil && address == nil {
				address = value
			}
		}
	}
	websiteNode := findByID(document, "ucUvjeti_litIzvodjacUrl")
	if websiteNode == nil {
		websiteNode = findByID(document, "ucUvjeti_litNositeljUrl")
	}
	website := any(nil)
	if href := attr(websiteNode, "href"); href != "" {
		website = href
	}
	logoNode := findFirst(document, func(node *html.Node) bool {
		return node.Type == html.ElementNode && node.Data == "img" && strings.Contains(attr(node, "src"), "ImageHandler.ashx")
	})
	logo := any(nil)
	if src := attr(logoNode, "src"); src != "" {
		logo = absoluteURL(src)
	}
	return map[string]any{"nositelj": nodeText(h1[0]), "izvodjac": provider, "adresa": address, "telefon": phone, "email": email, "web": website, "logo_url": logo}, originalName, nil
}

func absoluteURL(value string) string {
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return value
	}
	base, _ := url.Parse(detailsURL)
	reference, _ := url.Parse(value)
	return base.ResolveReference(reference).String()
}

func tableSnapshotFor(document *html.Node, section string) tableSnapshot {
	table := findByID(document, tableIDs[section])
	snapshot := tableSnapshot{TableID: tableIDs[section], Header: []string{}, Rows: []tableRow{}}
	if table == nil {
		return snapshot
	}
	captionNode := findFirst(table, func(node *html.Node) bool { return node.Type == html.ElementNode && node.Data == "caption" })
	if caption := nodeText(captionNode); caption != "" {
		snapshot.Caption = &caption
	}
	var rows []*html.Node
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "tr" {
			rows = append(rows, node)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(table)
	firstIsHeader := false
	for rowIndex, row := range rows {
		cells := directChildren(row, "th", "td")
		if len(cells) == 0 {
			continue
		}
		values := make([]string, len(cells))
		for index, cell := range cells {
			values[index] = nodeText(cell)
		}
		if rowIndex == 0 {
			firstIsHeader = len(directChildren(row, "th")) > 0
			if firstIsHeader {
				snapshot.Header = values
				continue
			}
		}
		snapshot.Rows = append(snapshot.Rows, tableRow{Index: len(snapshot.Rows) + 1, Cells: values})
	}
	_ = firstIsHeader
	return snapshot
}

func noteDocumentFor(document *html.Node, section string) noteDocument {
	node := findByID(document, noteIDs[section])
	doc := noteDocument{Blocks: []noteBlock{}}
	if node == nil {
		return doc
	}
	text := nodeTextLines(node)
	cleaned := cleanText(text)
	if cleaned != "" {
		doc.RawText = &cleaned
	}
	lines := make([]string, 0)
	for _, line := range strings.Split(text, "\n") {
		if value := cleanText(line); value != "" {
			lines = append(lines, value)
		}
	}
	var current *noteBlock
	for _, line := range lines {
		match := regexp.MustCompile(`^(\*{1,3})\s*(.*)$`).FindStringSubmatch(line)
		if len(match) > 0 {
			if current != nil {
				current.Text = strings.TrimSpace(current.Text)
				doc.Blocks = append(doc.Blocks, *current)
			}
			marker := match[1]
			current = &noteBlock{Marker: &marker, Text: strings.TrimSpace(match[2])}
		} else if current == nil {
			current = &noteBlock{Text: line}
		} else if current.Text == "" {
			current.Text = line
		} else {
			current.Text += "\n" + line
		}
	}
	if current != nil {
		current.Text = strings.TrimSpace(current.Text)
		doc.Blocks = append(doc.Blocks, *current)
	}
	return doc
}

func parseQuota(document *html.Node) map[string]any {
	// The source uses adjacent span nodes for labels and values. BeautifulSoup's
	// original `get_text(" ")` inserted boundaries between those nodes; use the
	// equivalent here so quota parsing does not produce `0Upisna` or swallow the
	// next label.
	value := nodeTextSeparated(findByID(document, "ucUvjeti_pnlOsnovniPodaci"))
	findInt := func(pattern string) any {
		match := regexp.MustCompile(pattern).FindStringSubmatch(value)
		if len(match) == 0 {
			return nil
		}
		result, _ := strconv.Atoi(match[1])
		return result
	}
	participation := trimQuotaValue(regexSub(value, `(?i)(?:Iznos maksimalne )?participacije\s*:\s*(.*)`))
	threshold := trimQuotaValue(regexSub(value, `(?i)Ukupni prag na razredbenom postupku\s*:\s*(.*)`))
	thresholdPct := any(nil)
	if threshold != "" {
		thresholdPct = percent(threshold)
	}
	return map[string]any{"eu": findInt(`(?i)Upisna kvota za državljane EU\s*:\s*(\d+)`), "strani": findInt(`(?i)Upisna kvota za strane državljane\s*:\s*(\d+)`), "participacija": nilIfEmptyAny(participation), "ukupni_prag_pct": thresholdPct, "izvor_sadrzi_eu_kvotu": regexp.MustCompile(`(?i)Upisna kvota za državljane EU`).MatchString(value), "izvor_sadrzi_stranu_kvotu": regexp.MustCompile(`(?i)Upisna kvota za strane državljane`).MatchString(value)}
}

func nodeTextSeparated(node *html.Node) string {
	if node == nil {
		return ""
	}
	parts := []string{}
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			parts = append(parts, current.Data)
			return
		}
		if current.Type == html.ElementNode && current.Data == "br" {
			parts = append(parts, " ")
			return
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return cleanText(strings.Join(parts, " "))
}

func nodeTextNoBreak(node *html.Node) string {
	if node == nil {
		return ""
	}
	var builder strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			builder.WriteString(current.Data)
			return
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return cleanText(builder.String())
}

func regexSub(value, pattern string) string {
	match := regexp.MustCompile(pattern).FindStringSubmatch(value)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

func trimQuotaValue(value string) string {
	value = strings.TrimSpace(value)
	for _, marker := range []string{" Ukupni prag", " Rok za prijavu"} {
		if index := strings.Index(value, marker); index >= 0 {
			value = strings.TrimSpace(value[:index])
		}
	}
	return value
}

func parsePreconditions(snapshot tableSnapshot) []map[string]any {
	result := []map[string]any{}
	for _, row := range snapshot.Rows {
		if len(row.Cells) > 0 && row.Cells[0] != "" {
			result = append(result, map[string]any{"tekst": row.Cells[0], "uvjet_primjene": true})
		}
	}
	return result
}

func parseRows(snapshot tableSnapshot, kind string) ([]map[string]any, error) {
	result := []map[string]any{}
	for _, row := range snapshot.Rows {
		cells := row.Cells
		scoredValue := func(index int) map[string]any { return scored(cells, index) }
		switch kind {
		case "ocjene":
			if len(cells) != 2 {
				return nil, fmt.Errorf("ocjene row has %d cells", len(cells))
			}
			markers := markerPattern.FindAllString(cells[0], -1)
			var marker any
			if len(markers) > 0 {
				marker = markers[len(markers)-1]
			}
			result = append(result, merge(map[string]any{"redak": row.Index, "naziv": strings.TrimSpace(markerPattern.ReplaceAllString(cells[0], "")), "napomena_marker": marker}, scoredValue(1)))
		case "obvezni":
			if len(cells) != 4 {
				return nil, fmt.Errorf("obvezni row has %d cells", len(cells))
			}
			alternative := alternativeRule(cells[0])
			if alternative != nil {
				alternative = merge(map[string]any{"tip": "alternativa"}, alternative)
			}
			result = append(result, merge(map[string]any{"redak": row.Index, "predmet": cells[0], "razina": nilIfEmptyAny(cells[1]), "prag_pct": percent(cells[2]), "pravilo_bodovanja": alternative}, scoredValue(3)))
		case "natjecanja":
			if len(cells) != 9 {
				return nil, fmt.Errorf("natjecanja row has %d cells", len(cells))
			}
			result = append(result, merge(map[string]any{"redak": row.Index, "kategorija": cells[0], "disciplina": cells[1], "razred_od": cells[2], "razred_do": cells[3], "plasman_od": cells[4], "plasman_do": cells[5], "nagrada_od": cells[6], "nagrada_do": cells[7], "disciplina_pravilo": alternativeRule(cells[1])}, scoredValue(8)))
		case "sportasi":
			if len(cells) != 3 {
				return nil, fmt.Errorf("sportasi row has %d cells", len(cells))
			}
			result = append(result, merge(map[string]any{"redak": row.Index, "kategorija_od": cells[0], "kategorija_do": cells[1]}, scoredValue(2)))
		case "posebna_postignuca":
			if len(cells) != 2 {
				return nil, fmt.Errorf("posebna postignuca row has %d cells", len(cells))
			}
			result = append(result, merge(map[string]any{"redak": row.Index, "postignuce": cells[0]}, scoredValue(1)))
		case "ocjene_mature":
			if len(cells) != 5 {
				return nil, fmt.Errorf("matura row has %d cells", len(cells))
			}
			result = append(result, merge(map[string]any{"redak": row.Index, "ispit": cells[0], "razina": nilIfEmptyAny(cells[1]), "ocjena_od": parseIntAny(cells[2]), "ocjena_do": parseIntAny(cells[3])}, scoredValue(4)))
		}
	}
	return result, nil
}

func scored(cells []string, index int) map[string]any {
	value := ""
	if index >= 0 && index < len(cells) {
		value = cells[index]
	}
	parsed := scoreValue(value)
	return map[string]any{"vrednovanje_pct": parsed["pct"], "vrednovanje": parsed, "izravan_upis": boolValue(parsed["izravan_upis"])}
}

func scoreValue(value string) map[string]any {
	raw := cleanText(value)
	result := map[string]any{"kind": "unparsed"}
	if raw == "" {
		result["kind"] = "not_published"
	}
	if value := percent(raw); value != nil {
		result["kind"] = "percentage"
		result["pct"] = value
	}
	if strings.Contains(strings.ToLower(raw), "izravan upis") {
		result["kind"] = "direct_admission"
		result["izravan_upis"] = true
	} else if strings.Contains(strings.ToLower(raw), "ne vrjednuje") || strings.Contains(strings.ToLower(raw), "ne vrednuje") {
		result["kind"] = "not_scored"
		result["ne_vrednuje_se"] = true
	}
	markers := markerPattern.FindAllString(raw, -1)
	if len(markers) > 0 {
		result["marker"] = markers[len(markers)-1]
		if result["kind"] == "unparsed" {
			result["kind"] = "footnote_marker"
		}
	}
	return result
}

func percent(value string) any {
	match := percentPattern.FindStringSubmatch(value)
	if len(match) == 0 {
		return nil
	}
	result, err := strconv.ParseFloat(strings.ReplaceAll(match[1], ",", "."), 64)
	if err != nil {
		return nil
	}
	return result
}

func parseElective(snapshot tableSnapshot, note noteDocument) (map[string]any, error) {
	if len(snapshot.Rows) == 1 && len(snapshot.Rows[0].Cells) == 1 && strings.EqualFold(snapshot.Rows[0].Cells[0], "nije zahtjev studija") {
		return map[string]any{"nije_zahtjev": true, "najbolji_rezultat_pct": nil, "predmeti": []map[string]any{}, "napomena": note.RawText, "napomene_strukturirano": []map[string]any{}}, nil
	}
	rules := inferNotes(note, "izborni")
	rows := []map[string]any{}
	for _, row := range snapshot.Rows {
		cells := row.Cells
		if len(cells) != 4 {
			return nil, fmt.Errorf("izborni row has %d cells", len(cells))
		}
		value := scored(cells, 3)
		var linked []map[string]any
		marker := stringValue(value["vrednovanje"].(map[string]any)["marker"])
		for _, rule := range rules {
			if marker != "" && stringValue(rule["marker"]) == marker {
				linked = append(linked, rule)
			}
		}
		var rule any = nil
		if alternative := alternativeRule(cells[0]); alternative != nil {
			rule = merge(map[string]any{"tip": "alternativa"}, alternative)
			rule.(map[string]any)["povezane_napomene"] = linked
		} else if len(linked) > 0 {
			rule = linked[0]
		}
		rows = append(rows, merge(map[string]any{"redak": row.Index, "predmet": cells[0], "obavezan": boolOrNil(cells[1]), "prag_pct": percent(cells[2]), "pravilo_bodovanja": rule}, value))
	}
	var weights []float64
	for _, rule := range rules {
		if values, ok := rule["ponderi_po_rangu_pct"].([]float64); ok {
			weights = append(weights, values...)
		}
	}
	return map[string]any{"nije_zahtjev": false, "najbolji_rezultat_pct": firstFloat(weights), "predmeti": rows, "napomena": note.RawText, "napomene_strukturirano": rules, "obvezni_predmeti_iz_napomene": anyRule(rules, "obvezno"), "izborni_dio_obvezan_za_sve": anyRule(rules, "izborni_dio_obvezan_za_sve"), "najmanje_jedan_predmet_obvezan": anyMinimum(rules, 1), "prag_izuzece_prije_2010_ili_izvan_rh": anyRule(rules, "prag_izuzece_prije_2010_ili_izvan_rh"), "zamjena_dodatne_provjere": anyRule(rules, "zamjena_dodatne_provjere")}, nil
}

func parseAdditional(snapshot tableSnapshot) ([]map[string]any, []map[string]any, error) {
	result, groups := []map[string]any{}, []map[string]any{}
	for _, row := range snapshot.Rows {
		cells := row.Cells
		if len(cells) != 4 {
			return nil, nil, fmt.Errorf("additional row has %d cells", len(cells))
		}
		alternative := alternativeRule(cells[0])
		internalRaw := ""
		if index := strings.Index(strings.ToLower(cells[0]), "vrednovanje:"); index >= 0 {
			internalRaw = strings.TrimSpace(cells[0][index+len("vrednovanje:"):])
		}
		internal := map[string]any{"alternativa": alternative, "interno_vrednovanje": parseInternalValues(internalRaw), "kumulativno": regexp.MustCompile(`(?i)kumulativno`).MatchString(cells[0]), "iskljucujuci_uvjet": nilIfEmptyAny(regexSub(cells[0], `(?i)(isključujući uvjet|iskljucujuci uvjet)\s*:?(.*)$`))}
		if alternative != nil && regexp.MustCompile(`(?i)\bi/ili\b`).MatchString(cells[0]) {
			alternative["terms"] = alternativeTerms(cells[0])
		}
		if regexp.MustCompile(`(?i)isključujući uvjet|iskljucujuci uvjet`).MatchString(cells[0]) {
			// The original custom parser preserved the complete row text here,
			// rather than only the matched words.
			internal["iskljucujuci_uvjet"] = cells[0]
		}
		item := merge(map[string]any{"redak": row.Index, "naziv": cells[0], "obavezan": boolOrNil(cells[1]), "prag_pct": percent(cells[2]), "uvjet_primjene": map[string]any{"tekst": cells[0], "uvjetno": regexp.MustCompile(`(?i)ukoliko|za strane državljane|za kandidate koji|\bako\b`).MatchString(cells[0])}, "unutarnja_pravila": internal}, scored(cells, 3))
		result = append(result, item)
		if alternative != nil || boolValue(internal["kumulativno"]) || stringValue(internal["iskljucujuci_uvjet"]) != "" {
			groups = append(groups, map[string]any{"tip": "pravilo_unutar_dodatne_provjere", "redci": []int{row.Index}, "alternativa": alternative, "kumulativno_bodovanje": internal["kumulativno"], "tekst": cells[0]})
		}
	}
	return result, groups, nil
}

func parseInternalValues(value string) []map[string]any {
	pattern := regexp.MustCompile(`([\p{L}][^,;.]*?)\s+(\d+(?:[.,]\d+)?)\s*%`)
	result := []map[string]any{}
	for _, match := range pattern.FindAllStringSubmatch(value, -1) {
		pctValue, _ := strconv.ParseFloat(strings.ReplaceAll(match[2], ",", "."), 64)
		result = append(result, map[string]any{"tekst": strings.TrimSpace(match[1]), "pct": pctValue})
	}
	return result
}

func alternativeRule(value string) map[string]any {
	cleaned := cleanText(value)
	if !regexp.MustCompile(`(?i)\s+ili\s+|\bi/ili\b`).MatchString(cleaned) {
		return nil
	}
	if regexp.MustCompile(`(?i)\bi/ili\b`).MatchString(cleaned) {
		parts := splitRegex(cleaned, `(?i)\s+i/ili\s+`)
		return map[string]any{"operator": "at_least_one_of", "terms": parts, "minimalno_odabranih": 1, "maksimalno_odabranih": len(parts), "mogu_se_odabrati_obje_varijante": true, "tekst": cleaned}
	}
	if regexp.MustCompile(`(?i)\b(kandidat\w*|pristupnik\w*|državljan\w*|završ\w*|zavrs\w*|ukoliko|ako)\b|:`).MatchString(cleaned) {
		return nil
	}
	parts := splitRegex(cleaned, `(?i)\s+ili\s+`)
	return map[string]any{"operator": "one_of", "terms": parts, "minimalno_odabranih": 1, "maksimalno_odabranih": 1, "ne_zbrajati_alternative": true, "tekst": cleaned}
}

func alternativeTerms(value string) []string {
	cleaned := cleanText(value)
	withoutExplanation := regexp.MustCompile(`(?i)^(.+?)\s+i/ili\s+([^()]+?)\s*\(.*$`).FindStringSubmatch(cleaned)
	if len(withoutExplanation) == 3 {
		return []string{strings.TrimSpace(withoutExplanation[1]), strings.TrimSpace(withoutExplanation[2])}
	}
	return splitRegex(cleaned, `(?i)\s+i/ili\s+`)
}

func splitRegex(value, pattern string) []string {
	parts := regexp.MustCompile(pattern).Split(value, -1)
	result := []string{}
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func inferNotes(note noteDocument, section string) []map[string]any {
	result := []map[string]any{}
	for _, block := range note.Blocks {
		result = append(result, inferRule(block.Text, section, block.Marker))
	}
	return result
}

func inferRule(value, section string, marker *string) map[string]any {
	raw, lower := cleanText(value), strings.ToLower(cleanText(value))
	result := map[string]any{"tip": "izvorna_napomena", "tekst": raw}
	if strings.Contains(lower, "za sve kandidate") {
		result["opseg"] = "svi_kandidati"
	}
	if marker != nil {
		result["marker"] = *marker
	}
	weights := rankWeights(raw)
	if len(weights) > 0 {
		result["strategija"] = "sortiraj_rezultate_silazno_i_primijeni_pondere_po_rangu"
		result["ponderi_po_rangu_pct"] = weights
		result["maksimalno_bodovanih"] = len(weights)
	}
	if maximum := maximumCount(raw); maximum != nil {
		result["maksimalno_polozenih"] = *maximum
	}
	if count := notedCount(raw); count != nil {
		result["broj_navedenih_alternativa"] = *count
	}
	optional := regexp.MustCompile(`(?i)ne mora|nije dužan|nije duzna|nije obvezan`).MatchString(lower)
	required := regexp.MustCompile(`(?i)dužan|duzna|mora položiti|obvezno`).MatchString(lower) && !optional
	if optional {
		result["obvezno"] = false
		result["minimalno_polozenih"] = 0
	} else if required {
		result["obvezno"] = true
		if regexp.MustCompile(`(?i)jedan|jednoga|jednog|jednu|jedno`).MatchString(lower) {
			result["minimalno_polozenih"] = 1
		}
	}
	if strings.Contains(lower, "polaganje ispita izbornog dijela") && strings.Contains(lower, "obvezno") {
		result["izborni_dio_obvezan_za_sve"] = true
	}
	if strings.Contains(lower, "polaganje ispita obveznog dijela") && strings.Contains(lower, "obvezno") {
		result["obvezni_dio_obvezan_za_sve"] = true
	}
	if strings.Contains(lower, "prag ne vrijedi") {
		result["prag_izuzece_prije_2010_ili_izvan_rh"] = true
	}
	if strings.Contains(lower, "umjesto ispita državne mature") && strings.Contains(lower, "dodatnoj provjeri") {
		result["zamjena_dodatne_provjere"] = true
	}
	if strings.Contains(lower, "vrijedi 5 godina") {
		result["vremensko_pravilo"] = raw
	}
	if index := strings.Index(lower, "ne priznaju se sljedeća natjecanja"); index >= 0 {
		if colon := strings.Index(raw[index:], ":"); colon >= 0 {
			result["iskljucena_natjecanja"] = strings.TrimSpace(raw[index+colon+1:])
		}
	}
	maximum := regexp.MustCompile(`(?i)najveći postotak.*?iznosi\s*([\d.,]+)\s*%`).FindStringSubmatch(raw)
	if len(maximum) > 1 {
		value, _ := strconv.ParseFloat(strings.ReplaceAll(maximum[1], ",", "."), 64)
		result["tip"] = "zajednicki_maksimum"
		result["strategija"] = "zbroj_oznacenih_stavki_ogranici_na_maksimum"
		result["maksimalno_pct"] = value
	}
	threshold := regexp.MustCompile(`(?i)ukupni prag.*?:\s*([\d.,]+)\s*%`).FindStringSubmatch(raw)
	if len(threshold) > 1 {
		value, _ := strconv.ParseFloat(strings.ReplaceAll(threshold[1], ",", "."), 64)
		result["ukupni_prag_pct"] = value
	}
	if section == "izborni" && !hasAny(result, "ponderi_po_rangu_pct", "izborni_dio_obvezan_za_sve", "obvezno") {
		result["sigurna_automatizacija_nije_moguca"] = true
	}
	return result
}

func rankWeights(value string) []float64 {
	positions := map[string]int{"najbolji": 0, "prvi": 0, "drugi": 1, "treći": 2, "treci": 2, "četvrti": 3, "cetvrti": 3}
	pattern := regexp.MustCompile(`(?i)\b(najbolji|prvi|drugi|treći|treci|četvrti|cetvrti)\s+[^%]{0,100}?([\d.,]+)\s*%`)
	values := map[int]float64{}
	for _, match := range pattern.FindAllStringSubmatch(value, -1) {
		parsed, _ := strconv.ParseFloat(strings.ReplaceAll(match[2], ",", "."), 64)
		values[positions[strings.ToLower(match[1])]] = parsed
	}
	result := []float64{}
	for index := 0; ; index++ {
		value, ok := values[index]
		if !ok {
			break
		}
		result = append(result, value)
	}
	return result
}

func notedCount(value string) *int {
	word := `jedan|jednog|jednoga|jednu|jedno|dva|dvije|tri|četiri|cetiri|pet|šest|sest|sedam|osam|devet|deset|jedanaest|dvanaest|trinaest|četrnaest|cetrnaest|petnaest|šesnaest|sesnaest|sedamnaest|osamnaest|devetnaest|dvadeset|\d+`
	for _, pattern := range []string{`(?i)\b(?:` + word + `)\s+od\s+([\p{L}\d]+)\s+naveden`, `(?i)\b(` + word + `)\s+naveden`} {
		match := regexp.MustCompile(pattern).FindStringSubmatch(value)
		if len(match) > 1 {
			if parsed := parseWordNumber(match[1]); parsed != nil {
				return parsed
			}
		}
	}
	return nil
}

func maximumCount(value string) *int {
	patterns := []string{`(?i)jedan\s+ili\s+(dva|dvije|tri|četiri|cetiri|\d+)\s+predmet`, `(?i)jednog\s+ili\s+(dva|dvije|tri|četiri|cetiri|\d+)\s+predmet`, `(?i)jedan\s+ili\s+sva\s+(dva|dvije|tri|četiri|cetiri|\d+)`, `(?i)sva\s+(dva|dvije|tri|četiri|cetiri|\d+)`, `(?i)bilo\s+koja\s+(dva|dvije|tri|četiri|cetiri|\d+)`}
	for _, pattern := range patterns {
		match := regexp.MustCompile(pattern).FindStringSubmatch(value)
		if len(match) > 1 {
			if parsed := parseWordNumber(match[1]); parsed != nil {
				return parsed
			}
		}
	}
	return nil
}

func parseWordNumber(value string) *int {
	if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
		return &parsed
	}
	if parsed, ok := numberWords[strings.ToLower(strings.TrimSpace(value))]; ok {
		return &parsed
	}
	return nil
}

func groupRules(rows []map[string]any, key func(map[string]any) string) []map[string]any {
	groups := map[string][]map[string]any{}
	order := []string{}
	for _, row := range rows {
		value := key(row)
		if value != "" {
			if _, exists := groups[value]; !exists {
				order = append(order, value)
			}
			groups[value] = append(groups[value], row)
		}
	}
	result := []map[string]any{}
	for _, group := range order {
		rows := groups[group]
		if len(rows) < 2 {
			continue
		}
		indices := []int{}
		for index, row := range rows {
			redak := intValue(row["redak"])
			if redak == 0 {
				redak = index + 1
			}
			indices = append(indices, redak)
		}
		result = append(result, map[string]any{"tip": "alternativni_redci_unutar_grupe", "grupa": group, "redci": indices, "strategija": "pojedinacni_rezultat", "redci_iste_grupe_se_ne_zbrajaju": true, "ne_zbrajati_alternative": true})
	}
	return result
}

func auditDetail(detail map[string]any, notes map[string]noteDocument) []map[string]any {
	limitations := []map[string]any{}
	add := func(kind, section, impact string, extra map[string]any) {
		item := map[string]any{"tip": kind, "sekcija": section, "utjecaj_na_kalkulator": impact}
		for key, value := range extra {
			item[key] = value
		}
		limitations = append(limitations, item)
	}
	sections := []struct {
		name string
		rows []map[string]any
	}{{"obvezni", mapsFrom(detail["obvezni"])}, {"izborni", mapsFrom(mapValue(detail["izborni"])["predmeti"])}, {"dodatne_vjestine", mapsFrom(detail["dodatne_vjestine"])}, {"natjecanja", mapsFrom(detail["natjecanja"])}, {"sportasi", mapsFrom(detail["sportasi"])}, {"druga_posebna_postignuca", mapsFrom(detail["druga_posebna_postignuca"])}}
	for _, section := range sections {
		for index, row := range section.rows {
			value := mapValue(row["vrednovanje"])
			kind := stringValue(value["kind"])
			if (kind == "footnote_marker" || kind == "unparsed") && len(noteBlocksFor(notes, section.name, stringValue(value["marker"]))) == 0 {
				add("nepoznato_vrednovanje", section.name, "Redak nije sigurno pretvoriv u numeričko bodovanje bez ručne provjere.", map[string]any{"redak": index + 1})
			}
		}
	}
	for _, row := range mapsFrom(detail["preduvjeti"]) {
		text := stringValue(row["tekst"])
		if regexp.MustCompile(`(?i)(^|[^\p{L}])ili([^\p{L}]|$)|ukoliko|(^|[^\p{L}])ako([^\p{L}]|$)|pod uvjetom|kandidat`).MatchString(text) {
			add("preduvjet_prirodni_jezik", "preduvjeti", "Uvjetni/alternativni prirodni jezik zadržan je kao tekst i traži ručnu potvrdu.", map[string]any{"tekst": text, "sigurna_automatizacija_nije_moguca": true})
		}
	}
	for section, document := range notes {
		for _, block := range document.Blocks {
			rule := inferRule(block.Text, section, block.Marker)
			if boolValue(rule["sigurna_automatizacija_nije_moguca"]) {
				add("slozena_napomena", section, "Napomena je samo djelomično strukturirana; ne koristiti neprepoznati dio kao formulu.", map[string]any{"marker": block.Marker, "tekst": block.Text})
			}
		}
	}
	electiveRows := mapsFrom(mapValue(detail["izborni"])["predmeti"])
	for _, block := range notes["izborni"].Blocks {
		rule := inferRule(block.Text, "izborni", block.Marker)
		expected, hasExpected := rule["broj_navedenih_alternativa"]
		if !hasExpected || stringValue(rule["opseg"]) == "svi_kandidati" {
			continue
		}
		actual := 0
		for _, row := range electiveRows {
			value := mapValue(row["vrednovanje"])
			marker := stringValue(value["marker"])
			blockMarker := ""
			if block.Marker != nil {
				blockMarker = *block.Marker
			}
			if marker != blockMarker {
				continue
			}
			predmet := stringValue(row["predmet"])
			if regexp.MustCompile(`(?i)\s+ili\s+`).MatchString(predmet) {
				actual += len(splitRegex(predmet, `(?i)\s+ili\s+`))
			} else {
				actual++
			}
		}
		if actual > 0 && actual != intValue(expected) {
			add("napomena_opseg_neusklađen", "izborni", "Napomena navodi više alternativa nego što ih nosi označeni redak; vjerojatno se odnosi na širi skup redaka ili je izvor nedosljedan.", map[string]any{"marker": block.Marker, "tekst": block.Text, "ocekivano": intValue(expected), "vidljivo_u_oznacenom_retku": actual})
		}
	}
	return limitations
}

func noteBlocksFor(notes map[string]noteDocument, section, marker string) []noteBlock {
	document := notes[section]
	result := []noteBlock{}
	for _, block := range document.Blocks {
		blockMarker := ""
		if block.Marker != nil {
			blockMarker = *block.Marker
		}
		if blockMarker == marker {
			result = append(result, block)
		}
	}
	return result
}

func nilIfEmptyAny(value any) any {
	if value == nil || stringValue(value) == "" {
		return nil
	}
	return value
}
func parseIntAny(value string) any {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return nil
	}
	return parsed
}
func boolOrNil(value string) any {
	switch strings.ToLower(cleanText(value)) {
	case "da":
		return true
	case "ne":
		return false
	default:
		return nil
	}
}
func boolValue(value any) bool { result, ok := value.(bool); return ok && result }
func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if result, ok := value.(string); ok {
		return result
	}
	if result, ok := value.(*string); ok {
		if result == nil {
			return ""
		}
		return *result
	}
	return fmt.Sprint(value)
}
func intValue(value any) int {
	switch number := value.(type) {
	case int:
		return number
	case float64:
		return int(number)
	default:
		parsed, _ := strconv.Atoi(stringValue(value))
		return parsed
	}
}
func mapValue(value any) map[string]any { result, _ := value.(map[string]any); return result }
func mapsFrom(value any) []map[string]any {
	if value == nil {
		return nil
	}
	values, _ := value.([]map[string]any)
	return values
}
func merge(values ...map[string]any) map[string]any {
	result := map[string]any{}
	for _, value := range values {
		for key, item := range value {
			result[key] = item
		}
	}
	return result
}
func cloneMap(value map[string]any) map[string]any {
	result := map[string]any{}
	for key, item := range value {
		result[key] = item
	}
	return result
}
func nilIfEmpty(value []map[string]any) any {
	if len(value) == 0 {
		return nil
	}
	return value
}
func anyRule(rules []map[string]any, key string) bool {
	for _, rule := range rules {
		if boolValue(rule[key]) {
			return true
		}
	}
	return false
}
func anyMinimum(rules []map[string]any, minimum int) bool {
	for _, rule := range rules {
		if intValue(rule["minimalno_polozenih"]) >= minimum {
			return true
		}
	}
	return false
}
func hasAny(value map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := value[key]; ok {
			return true
		}
	}
	return false
}
func firstFloat(values []float64) any {
	if len(values) == 0 {
		return nil
	}
	return values[0]
}
func limitationTypes(values []map[string]any) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if key := stringValue(value["tip"]); key != "" {
			result = append(result, key)
		}
	}
	return result
}
