package main

// This is the publication boundary for source/programi.json. The parser uses
// maps internally because the HTML has several deliberately different row
// shapes, but the emitted document is checked here as a strict, typed contract.
// Keeping this validator local avoids an additional dependency while still
// providing the useful part of a Zod-like workflow: unknown fields, missing
// fields, and type changes are reported with JSON paths.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
)

const maxStudySchemaIssues = 100

type studySchemaValidator struct {
	issues []string
	total  int
}

type studySchemaError struct {
	issues []string
	total  int
}

func (e *studySchemaError) Error() string {
	if len(e.issues) == 0 {
		return "study schema validation failed"
	}
	message := fmt.Sprintf("study schema validation failed with %d issue(s)", e.total)
	for _, issue := range e.issues {
		message += "\n- " + issue
	}
	if e.total > len(e.issues) {
		message += fmt.Sprintf("\n- additional issues omitted after the first %d", maxStudySchemaIssues)
	}
	return message
}

func validateStudyCatalogFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read study catalog for validation: %w", err)
	}
	return validateStudyCatalogJSON(data)
}

func validateStudyCatalogJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("decode study catalog for validation: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode study catalog for validation: multiple JSON values")
		}
		return fmt.Errorf("decode study catalog for validation: trailing data: %w", err)
	}

	validator := &studySchemaValidator{}
	validator.validateRoot(value)
	if validator.total > 0 {
		return &studySchemaError{issues: validator.issues, total: validator.total}
	}
	return nil
}

func (v *studySchemaValidator) issue(path, message string) {
	v.total++
	if len(v.issues) < maxStudySchemaIssues {
		v.issues = append(v.issues, path+": "+message)
	}
}

func (v *studySchemaValidator) object(path string, value any, required, allowed []string) map[string]any {
	object, ok := value.(map[string]any)
	if !ok {
		v.issue(path, "expected object, got "+jsonType(value))
		return nil
	}
	allowedSet := stringSet(allowed)
	for key := range object {
		if !allowedSet[key] {
			v.issue(path+"."+key, "unknown field")
		}
	}
	for _, key := range required {
		if _, exists := object[key]; !exists {
			v.issue(path+"."+key, "missing required field")
		}
	}
	return object
}

func (v *studySchemaValidator) array(path string, value any) []any {
	items, ok := value.([]any)
	if !ok {
		v.issue(path, "expected array, got "+jsonType(value))
		return nil
	}
	return items
}

func (v *studySchemaValidator) field(object map[string]any, path, key string, validate func(string, any)) {
	value, exists := object[key]
	if exists {
		validate(path+"."+key, value)
	}
}

func (v *studySchemaValidator) each(path string, value any, validate func(string, any)) {
	for index, item := range v.array(path, value) {
		validate(fmt.Sprintf("%s[%d]", path, index), item)
	}
}

func (v *studySchemaValidator) validateRoot(value any) {
	root := v.object("$", value, []string{"generated_at", "refresh_schedule", "programi"}, []string{"generated_at", "refresh_schedule", "programi"})
	if root == nil {
		return
	}
	v.field(root, "$", "generated_at", v.string)
	v.field(root, "$", "refresh_schedule", v.stringArray)
	v.field(root, "$", "programi", func(path string, value any) {
		v.each(path, value, v.validateProgram)
	})
}

func (v *studySchemaValidator) validateProgram(path string, value any) {
	program := v.object(path, value,
		[]string{"detalji", "ects", "id", "idPrograma", "izvodjac", "mjesto", "modul", "naziv", "nositelj", "pretraga", "smjer", "trajanje_god", "vrsta_studija"},
		[]string{"detalji", "ects", "id", "idPrograma", "izvodjac", "mjesto", "modul", "naziv", "nositelj", "pretraga", "smjer", "trajanje_god", "vrsta_studija"})
	if program == nil {
		return
	}
	for _, key := range []string{"ects", "id", "idPrograma"} {
		v.field(program, path, key, v.integer)
	}
	v.field(program, path, "trajanje_god", v.number)
	v.field(program, path, "vrsta_studija", v.nullableInteger)
	for _, key := range []string{"izvodjac", "mjesto", "naziv", "nositelj"} {
		v.field(program, path, key, v.string)
	}
	v.field(program, path, "modul", v.stringArray)
	v.field(program, path, "smjer", v.stringArray)
	v.field(program, path, "pretraga", v.validateSearchRelation)
	v.field(program, path, "detalji", v.validateDetail)
}

func (v *studySchemaValidator) validateSearchRelation(path string, value any) {
	object := v.object(path, value, []string{"sastavnica_id", "podrucja", "polja", "posebna_kvota", "redoslijed"}, []string{"sastavnica_id", "podrucja", "polja", "posebna_kvota", "redoslijed"})
	if object == nil {
		return
	}
	v.field(object, path, "sastavnica_id", v.string)
	v.field(object, path, "podrucja", v.stringArray)
	v.field(object, path, "polja", v.stringArray)
	v.field(object, path, "posebna_kvota", v.string)
	v.field(object, path, "redoslijed", v.integer)
}

func (v *studySchemaValidator) validateDetail(path string, value any) {
	detail := v.object(path, value,
		[]string{"dodatne_provjere_grupe", "dodatne_vjestine", "druga_posebna_postignuca", "hrvatski_jezik", "idPrograma", "izborni", "kalkulator_spremnost", "kvota", "medusekcijska_pravila", "natjecanja", "natjecanja_pravila", "obvezni", "obvezni_pravila", "ocjene", "ogranicenja_izvora", "osnovno", "posebna_postignuca_pravila", "prag", "preduvjeti", "sportasi", "vrednovanje_ocjena_mature", "vrednovanje_ocjena_mature_pravila"},
		[]string{"dodatne_provjere_grupe", "dodatne_vjestine", "druga_posebna_postignuca", "hrvatski_jezik", "idPrograma", "izborni", "kalkulator_spremnost", "kvota", "medusekcijska_pravila", "natjecanja", "natjecanja_pravila", "obvezni", "obvezni_pravila", "ocjene", "ogranicenja_izvora", "osnovno", "posebna_postignuca_pravila", "prag", "preduvjeti", "sportasi", "vrednovanje_ocjena_mature", "vrednovanje_ocjena_mature_pravila"})
	if detail == nil {
		return
	}
	v.field(detail, path, "idPrograma", v.integer)
	v.field(detail, path, "osnovno", v.validateBasic)
	v.field(detail, path, "kvota", v.validateQuota)
	v.field(detail, path, "preduvjeti", func(itemPath string, item any) {
		v.each(itemPath, item, v.validatePrecondition)
	})
	v.field(detail, path, "ocjene", func(itemPath string, item any) {
		v.each(itemPath, item, v.validateGradeRow)
	})
	v.field(detail, path, "hrvatski_jezik", v.validateCroatian)
	v.field(detail, path, "prag", v.validateThreshold)
	v.field(detail, path, "obvezni", func(itemPath string, item any) {
		v.each(itemPath, item, v.validateMandatoryRow)
	})
	v.field(detail, path, "obvezni_pravila", v.validateNullableRuleArray)
	v.field(detail, path, "izborni", v.validateElective)
	v.field(detail, path, "dodatne_vjestine", func(itemPath string, item any) {
		v.each(itemPath, item, v.validateAdditionalRow)
	})
	v.field(detail, path, "dodatne_provjere_grupe", func(itemPath string, item any) {
		v.each(itemPath, item, v.validateAdditionalGroup)
	})
	v.field(detail, path, "natjecanja", func(itemPath string, item any) {
		v.each(itemPath, item, v.validateCompetitionRow)
	})
	v.field(detail, path, "natjecanja_pravila", v.validateNullableRuleArray)
	v.field(detail, path, "sportasi", func(itemPath string, item any) {
		v.each(itemPath, item, v.validateAthleteRow)
	})
	v.field(detail, path, "druga_posebna_postignuca", func(itemPath string, item any) {
		v.each(itemPath, item, v.validateAchievementRow)
	})
	v.field(detail, path, "posebna_postignuca_pravila", v.validateNullableRuleArray)
	v.field(detail, path, "vrednovanje_ocjena_mature", func(itemPath string, item any) {
		v.each(itemPath, item, v.validateMaturityRow)
	})
	v.field(detail, path, "vrednovanje_ocjena_mature_pravila", v.validateNullableRuleArray)
	v.field(detail, path, "medusekcijska_pravila", func(itemPath string, item any) {
		v.each(itemPath, item, v.validateRule)
	})
	v.field(detail, path, "ogranicenja_izvora", func(itemPath string, item any) {
		v.each(itemPath, item, v.validateLimitation)
	})
	v.field(detail, path, "kalkulator_spremnost", v.validateCalculatorReadiness)
}

func (v *studySchemaValidator) validateBasic(path string, value any) {
	object := v.object(path, value, []string{"adresa", "email", "izvodjac", "logo_url", "nositelj", "telefon", "web"}, []string{"adresa", "email", "izvodjac", "logo_url", "nositelj", "telefon", "web"})
	if object == nil {
		return
	}
	for _, key := range []string{"adresa", "email", "izvodjac", "logo_url", "nositelj", "telefon", "web"} {
		v.field(object, path, key, v.nullableString)
	}
}

func (v *studySchemaValidator) validateQuota(path string, value any) {
	object := v.object(path, value, []string{"eu", "izvor_sadrzi_eu_kvotu", "izvor_sadrzi_stranu_kvotu", "participacija", "strani", "ukupni_prag_pct"}, []string{"eu", "izvor_sadrzi_eu_kvotu", "izvor_sadrzi_stranu_kvotu", "participacija", "strani", "ukupni_prag_pct"})
	if object == nil {
		return
	}
	v.field(object, path, "eu", v.nullableInteger)
	v.field(object, path, "strani", v.nullableInteger)
	v.field(object, path, "participacija", v.nullableString)
	v.field(object, path, "ukupni_prag_pct", v.nullableNumber)
	v.field(object, path, "izvor_sadrzi_eu_kvotu", v.boolean)
	v.field(object, path, "izvor_sadrzi_stranu_kvotu", v.boolean)
}

func (v *studySchemaValidator) validatePrecondition(path string, value any) {
	object := v.object(path, value, []string{"tekst", "uvjet_primjene"}, []string{"tekst", "uvjet_primjene"})
	if object == nil {
		return
	}
	v.field(object, path, "tekst", v.string)
	v.field(object, path, "uvjet_primjene", v.boolean)
}

func (v *studySchemaValidator) validateCroatian(path string, value any) {
	object := v.object(path, value, []string{"eu_izuzece", "obvezno_za_sve", "priznaje_a_razinu", "priznaje_b_razinu"}, []string{"eu_izuzece", "obvezno_za_sve", "priznaje_a_razinu", "priznaje_b_razinu"})
	if object == nil {
		return
	}
	for _, key := range []string{"eu_izuzece", "obvezno_za_sve", "priznaje_a_razinu", "priznaje_b_razinu"} {
		v.field(object, path, key, v.boolean)
	}
}

func (v *studySchemaValidator) validateCalculatorReadiness(path string, value any) {
	object := v.object(path, value, []string{"razlozi", "status", "zahtijeva_rucni_pregled"}, []string{"razlozi", "status", "zahtijeva_rucni_pregled"})
	if object == nil {
		return
	}
	v.field(object, path, "razlozi", v.stringArray)
	v.field(object, path, "status", v.string)
	v.field(object, path, "zahtijeva_rucni_pregled", v.boolean)
}

func (v *studySchemaValidator) validateGradeRow(path string, value any) {
	object := v.object(path, value, []string{"izravan_upis", "napomena_marker", "naziv", "redak", "vrednovanje", "vrednovanje_pct"}, []string{"izravan_upis", "napomena_marker", "naziv", "redak", "vrednovanje", "vrednovanje_pct"})
	if object == nil {
		return
	}
	v.field(object, path, "redak", v.integer)
	v.field(object, path, "naziv", v.string)
	v.field(object, path, "napomena_marker", v.nullableString)
	v.validateScoredFields(path, object)
}

func (v *studySchemaValidator) validateMandatoryRow(path string, value any) {
	object := v.object(path, value, []string{"izravan_upis", "prag_pct", "pravilo_bodovanja", "predmet", "razina", "redak", "vrednovanje", "vrednovanje_pct"}, []string{"izravan_upis", "prag_pct", "pravilo_bodovanja", "predmet", "razina", "redak", "vrednovanje", "vrednovanje_pct"})
	if object == nil {
		return
	}
	v.field(object, path, "redak", v.integer)
	v.field(object, path, "predmet", v.string)
	v.field(object, path, "razina", v.nullableString)
	v.field(object, path, "prag_pct", v.nullableNumber)
	v.field(object, path, "pravilo_bodovanja", v.validateNullableRule)
	v.validateScoredFields(path, object)
}

func (v *studySchemaValidator) validateAdditionalRow(path string, value any) {
	object := v.object(path, value, []string{"izravan_upis", "naziv", "obavezan", "prag_pct", "redak", "unutarnja_pravila", "uvjet_primjene", "vrednovanje", "vrednovanje_pct"}, []string{"izravan_upis", "naziv", "obavezan", "prag_pct", "redak", "unutarnja_pravila", "uvjet_primjene", "vrednovanje", "vrednovanje_pct"})
	if object == nil {
		return
	}
	v.field(object, path, "redak", v.integer)
	v.field(object, path, "naziv", v.string)
	v.field(object, path, "obavezan", v.nullableBoolean)
	v.field(object, path, "prag_pct", v.nullableNumber)
	v.field(object, path, "unutarnja_pravila", v.validateInternalRules)
	v.field(object, path, "uvjet_primjene", v.validateApplicationCondition)
	v.validateScoredFields(path, object)
}

func (v *studySchemaValidator) validateCompetitionRow(path string, value any) {
	object := v.object(path, value, []string{"disciplina", "disciplina_pravilo", "izravan_upis", "kategorija", "nagrada_do", "nagrada_od", "plasman_do", "plasman_od", "razred_do", "razred_od", "redak", "vrednovanje", "vrednovanje_pct"}, []string{"disciplina", "disciplina_pravilo", "izravan_upis", "kategorija", "nagrada_do", "nagrada_od", "plasman_do", "plasman_od", "razred_do", "razred_od", "redak", "vrednovanje", "vrednovanje_pct"})
	if object == nil {
		return
	}
	v.field(object, path, "disciplina", v.string)
	v.field(object, path, "disciplina_pravilo", v.validateNullableRule)
	v.field(object, path, "kategorija", v.string)
	for _, key := range []string{"nagrada_do", "nagrada_od", "plasman_do", "plasman_od", "razred_do", "razred_od"} {
		v.field(object, path, key, v.string)
	}
	v.field(object, path, "redak", v.integer)
	v.validateScoredFields(path, object)
}

func (v *studySchemaValidator) validateAthleteRow(path string, value any) {
	object := v.object(path, value, []string{"izravan_upis", "kategorija_do", "kategorija_od", "redak", "vrednovanje", "vrednovanje_pct"}, []string{"izravan_upis", "kategorija_do", "kategorija_od", "redak", "vrednovanje", "vrednovanje_pct"})
	if object == nil {
		return
	}
	for _, key := range []string{"kategorija_do", "kategorija_od"} {
		v.field(object, path, key, v.string)
	}
	v.field(object, path, "redak", v.integer)
	v.validateScoredFields(path, object)
}

func (v *studySchemaValidator) validateAchievementRow(path string, value any) {
	object := v.object(path, value, []string{"izravan_upis", "postignuce", "redak", "vrednovanje", "vrednovanje_pct"}, []string{"izravan_upis", "postignuce", "redak", "vrednovanje", "vrednovanje_pct"})
	if object == nil {
		return
	}
	v.field(object, path, "postignuce", v.string)
	v.field(object, path, "redak", v.integer)
	v.validateScoredFields(path, object)
}

func (v *studySchemaValidator) validateMaturityRow(path string, value any) {
	object := v.object(path, value, []string{"ispit", "izravan_upis", "ocjena_do", "ocjena_od", "razina", "redak", "vrednovanje", "vrednovanje_pct"}, []string{"ispit", "izravan_upis", "ocjena_do", "ocjena_od", "razina", "redak", "vrednovanje", "vrednovanje_pct"})
	if object == nil {
		return
	}
	v.field(object, path, "ispit", v.string)
	v.field(object, path, "ocjena_do", v.integer)
	v.field(object, path, "ocjena_od", v.integer)
	v.field(object, path, "razina", v.nullableString)
	v.field(object, path, "redak", v.integer)
	v.validateScoredFields(path, object)
}

func (v *studySchemaValidator) validateScoredFields(path string, object map[string]any) {
	v.field(object, path, "izravan_upis", v.boolean)
	v.field(object, path, "vrednovanje", v.validateScoreValue)
	v.field(object, path, "vrednovanje_pct", v.nullableNumber)
}

func (v *studySchemaValidator) validateScoreValue(path string, value any) {
	object := v.object(path, value, []string{"kind"}, []string{"kind", "pct", "marker", "izravan_upis", "ne_vrednuje_se"})
	if object == nil {
		return
	}
	v.field(object, path, "kind", func(fieldPath string, fieldValue any) {
		kind, ok := fieldValue.(string)
		if !ok {
			v.issue(fieldPath, "expected string, got "+jsonType(fieldValue))
			return
		}
		if !stringSet([]string{"percentage", "footnote_marker", "not_scored", "direct_admission", "not_published", "unparsed"})[kind] {
			v.issue(fieldPath, fmt.Sprintf("unknown score kind %q", kind))
		}
	})
	v.field(object, path, "pct", v.number)
	v.field(object, path, "marker", v.string)
	v.field(object, path, "izravan_upis", v.boolean)
	v.field(object, path, "ne_vrednuje_se", v.boolean)
}

func (v *studySchemaValidator) validateElective(path string, value any) {
	allowed := []string{"izborni_dio_obvezan_za_sve", "najbolji_rezultat_pct", "najmanje_jedan_predmet_obvezan", "napomena", "napomene_strukturirano", "nije_zahtjev", "obvezni_predmeti_iz_napomene", "prag_izuzece_prije_2010_ili_izvan_rh", "predmeti", "zamjena_dodatne_provjere"}
	elective := v.object(path, value, []string{"najbolji_rezultat_pct", "napomena", "napomene_strukturirano", "nije_zahtjev", "predmeti"}, allowed)
	if elective == nil {
		return
	}
	v.field(elective, path, "najbolji_rezultat_pct", v.nullableNumber)
	v.field(elective, path, "napomena", v.nullableString)
	v.field(elective, path, "napomene_strukturirano", v.validateRuleArray)
	v.field(elective, path, "nije_zahtjev", v.boolean)
	v.field(elective, path, "predmeti", func(itemPath string, item any) {
		v.each(itemPath, item, v.validateElectiveRow)
	})
	for _, key := range []string{"obvezni_predmeti_iz_napomene", "izborni_dio_obvezan_za_sve", "najmanje_jedan_predmet_obvezan", "prag_izuzece_prije_2010_ili_izvan_rh", "zamjena_dodatne_provjere"} {
		v.field(elective, path, key, v.boolean)
	}
}

func (v *studySchemaValidator) validateElectiveRow(path string, value any) {
	object := v.object(path, value, []string{"izravan_upis", "obavezan", "prag_pct", "pravilo_bodovanja", "predmet", "redak", "vrednovanje", "vrednovanje_pct"}, []string{"izravan_upis", "obavezan", "prag_pct", "pravilo_bodovanja", "predmet", "redak", "vrednovanje", "vrednovanje_pct"})
	if object == nil {
		return
	}
	v.field(object, path, "obavezan", v.nullableBoolean)
	v.field(object, path, "prag_pct", v.nullableNumber)
	v.field(object, path, "pravilo_bodovanja", v.validateNullableRule)
	v.field(object, path, "predmet", v.string)
	v.field(object, path, "redak", v.integer)
	v.validateScoredFields(path, object)
}

func (v *studySchemaValidator) validateAdditionalGroup(path string, value any) {
	object := v.object(path, value, []string{"alternativa", "kumulativno_bodovanje", "redci", "tekst", "tip"}, []string{"alternativa", "kumulativno_bodovanje", "redci", "tekst", "tip"})
	if object == nil {
		return
	}
	v.field(object, path, "alternativa", v.validateNullableAlternative)
	v.field(object, path, "kumulativno_bodovanje", v.boolean)
	v.field(object, path, "redci", v.integerArray)
	v.field(object, path, "tekst", v.string)
	v.field(object, path, "tip", v.string)
}

func (v *studySchemaValidator) validateInternalRules(path string, value any) {
	object := v.object(path, value, []string{"alternativa", "interno_vrednovanje", "iskljucujuci_uvjet", "kumulativno"}, []string{"alternativa", "interno_vrednovanje", "iskljucujuci_uvjet", "kumulativno"})
	if object == nil {
		return
	}
	v.field(object, path, "alternativa", v.validateNullableAlternative)
	v.field(object, path, "interno_vrednovanje", func(itemPath string, item any) {
		v.each(itemPath, item, v.validateInternalValue)
	})
	v.field(object, path, "iskljucujuci_uvjet", v.nullableString)
	v.field(object, path, "kumulativno", v.boolean)
}

func (v *studySchemaValidator) validateInternalValue(path string, value any) {
	object := v.object(path, value, []string{"pct", "tekst"}, []string{"pct", "tekst"})
	if object == nil {
		return
	}
	v.field(object, path, "pct", v.number)
	v.field(object, path, "tekst", v.string)
}

func (v *studySchemaValidator) validateApplicationCondition(path string, value any) {
	object := v.object(path, value, []string{"tekst", "uvjetno"}, []string{"tekst", "uvjetno"})
	if object == nil {
		return
	}
	v.field(object, path, "tekst", v.string)
	v.field(object, path, "uvjetno", v.boolean)
}

func (v *studySchemaValidator) validateThreshold(path string, value any) {
	if value == nil {
		return
	}
	if array, ok := value.([]any); ok {
		for index, item := range array {
			v.validateRule(fmt.Sprintf("%s[%d]", path, index), item)
		}
		return
	}
	v.validateRule(path, value)
}

var studyRuleFields = []string{
	"alternativa", "broj_navedenih_alternativa", "grupa", "iskljucena_natjecanja", "izborni_dio_obvezan_za_sve",
	"kumulativno_bodovanje", "maksimalno_bodovanih", "maksimalno_odabranih", "maksimalno_polozenih", "maksimalno_pct",
	"marker", "minimalno_odabranih", "minimalno_polozenih", "mogu_se_odabrati_obje_varijante", "ne_zbrajati_alternative", "ne_zbrajati_duplikat",
	"obvezni_dio_obvezan_za_sve", "obvezno", "operator", "opseg", "ponderi_po_rangu_pct", "povezane_napomene",
	"prag_izuzece_prije_2010_ili_izvan_rh", "redci", "redci_iste_grupe_se_ne_zbrajaju", "sekcije", "sigurna_automatizacija_nije_moguca",
	"strategija", "tekst", "terms", "tip", "ukupni_prag_pct", "vremensko_pravilo", "zamjena_dodatne_provjere", "zajednicki_maksimalno_pct",
}

func (v *studySchemaValidator) validateRule(path string, value any) {
	object := v.object(path, value, nil, studyRuleFields)
	if object == nil {
		return
	}
	for _, key := range []string{"tip", "operator", "marker", "tekst", "opseg", "grupa", "strategija", "iskljucena_natjecanja", "vremensko_pravilo"} {
		v.field(object, path, key, v.string)
	}
	for _, key := range []string{"obvezno", "mogu_se_odabrati_obje_varijante", "ne_zbrajati_alternative", "ne_zbrajati_duplikat", "kumulativno_bodovanje", "obvezni_dio_obvezan_za_sve", "izborni_dio_obvezan_za_sve", "prag_izuzece_prije_2010_ili_izvan_rh", "zamjena_dodatne_provjere", "sigurna_automatizacija_nije_moguca", "redci_iste_grupe_se_ne_zbrajaju"} {
		v.field(object, path, key, v.boolean)
	}
	for _, key := range []string{"broj_navedenih_alternativa", "maksimalno_bodovanih", "maksimalno_odabranih", "maksimalno_polozenih", "minimalno_odabranih", "minimalno_polozenih"} {
		v.field(object, path, key, v.integer)
	}
	for _, key := range []string{"maksimalno_pct", "ukupni_prag_pct", "zajednicki_maksimalno_pct"} {
		v.field(object, path, key, v.number)
	}
	for _, key := range []string{"terms", "sekcije"} {
		v.field(object, path, key, v.stringArray)
	}
	v.field(object, path, "ponderi_po_rangu_pct", v.numberArray)
	v.field(object, path, "redci", v.integerArray)
	v.field(object, path, "povezane_napomene", func(itemPath string, item any) {
		v.each(itemPath, item, v.validateRule)
	})
	v.field(object, path, "alternativa", v.validateNullableAlternative)
}

func (v *studySchemaValidator) validateAlternative(path string, value any) {
	// Mirrors parse_details.py exactly: i/ili publishes
	// mogu_se_odabrati_obje_varijante=true, while a plain ili publishes
	// ne_zbrajati_alternative=true. Both are meaningful, mutually exclusive
	// source representations of the same bounded-choice rule.
	object := v.object(path, value, []string{"maksimalno_odabranih", "minimalno_odabranih", "operator", "tekst", "terms"}, []string{"maksimalno_odabranih", "minimalno_odabranih", "mogu_se_odabrati_obje_varijante", "ne_zbrajati_alternative", "operator", "tekst", "terms"})
	if object == nil {
		return
	}
	canChooseBoth, hasCanChooseBoth := object["mogu_se_odabrati_obje_varijante"]
	doNotSum, hasDoNotSum := object["ne_zbrajati_alternative"]
	if hasCanChooseBoth == hasDoNotSum {
		v.issue(path, "requires exactly one alternative-mode field")
	}
	if hasCanChooseBoth {
		v.boolean(path+".mogu_se_odabrati_obje_varijante", canChooseBoth)
	}
	if hasDoNotSum {
		v.boolean(path+".ne_zbrajati_alternative", doNotSum)
	}
	v.field(object, path, "maksimalno_odabranih", v.integer)
	v.field(object, path, "minimalno_odabranih", v.integer)
	v.field(object, path, "operator", v.string)
	v.field(object, path, "tekst", v.string)
	v.field(object, path, "terms", v.stringArray)
}
func (v *studySchemaValidator) validateNullableAlternative(path string, value any) {
	if value != nil {
		v.validateAlternative(path, value)
	}
}

func (v *studySchemaValidator) validateNullableRule(path string, value any) {
	if value != nil {
		v.validateRule(path, value)
	}
}

func (v *studySchemaValidator) validateRuleArray(path string, value any) {
	v.each(path, value, v.validateRule)
}

func (v *studySchemaValidator) validateNullableRuleArray(path string, value any) {
	if value != nil {
		v.validateRuleArray(path, value)
	}
}

func (v *studySchemaValidator) validateLimitation(path string, value any) {
	allowed := []string{"marker", "ocekivano", "redak", "sekcija", "sigurna_automatizacija_nije_moguca", "tekst", "tip", "utjecaj_na_kalkulator", "vidljivo_u_oznacenom_retku"}
	object := v.object(path, value, []string{"sekcija", "tip", "utjecaj_na_kalkulator"}, allowed)
	if object == nil {
		return
	}
	v.field(object, path, "marker", v.string)
	v.field(object, path, "ocekivano", v.integer)
	v.field(object, path, "redak", v.integer)
	v.field(object, path, "sekcija", v.string)
	v.field(object, path, "sigurna_automatizacija_nije_moguca", v.boolean)
	v.field(object, path, "tekst", v.string)
	v.field(object, path, "tip", v.string)
	v.field(object, path, "utjecaj_na_kalkulator", v.string)
	v.field(object, path, "vidljivo_u_oznacenom_retku", v.integer)
}

func (v *studySchemaValidator) string(path string, value any) {
	if _, ok := value.(string); !ok {
		v.issue(path, "expected string, got "+jsonType(value))
	}
}

func (v *studySchemaValidator) nullableString(path string, value any) {
	if value != nil {
		v.string(path, value)
	}
}

func (v *studySchemaValidator) boolean(path string, value any) {
	if _, ok := value.(bool); !ok {
		v.issue(path, "expected boolean, got "+jsonType(value))
	}
}

func (v *studySchemaValidator) nullableBoolean(path string, value any) {
	if value != nil {
		v.boolean(path, value)
	}
}

func (v *studySchemaValidator) number(path string, value any) {
	parsed, ok := jsonNumber(value)
	if !ok || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		v.issue(path, "expected finite number, got "+jsonType(value))
	}
}

func (v *studySchemaValidator) nullableNumber(path string, value any) {
	if value != nil {
		v.number(path, value)
	}
}

func (v *studySchemaValidator) integer(path string, value any) {
	parsed, ok := jsonNumber(value)
	if !ok || math.IsNaN(parsed) || math.IsInf(parsed, 0) || math.Trunc(parsed) != parsed {
		v.issue(path, "expected integer, got "+jsonType(value))
	}
}

func (v *studySchemaValidator) nullableInteger(path string, value any) {
	if value != nil {
		v.integer(path, value)
	}
}

func (v *studySchemaValidator) stringArray(path string, value any) {
	for index, item := range v.array(path, value) {
		v.string(fmt.Sprintf("%s[%d]", path, index), item)
	}
}

func (v *studySchemaValidator) numberArray(path string, value any) {
	for index, item := range v.array(path, value) {
		v.number(fmt.Sprintf("%s[%d]", path, index), item)
	}
}

func (v *studySchemaValidator) integerArray(path string, value any) {
	for index, item := range v.array(path, value) {
		v.integer(fmt.Sprintf("%s[%d]", path, index), item)
	}
}

func jsonNumber(value any) (float64, bool) {
	switch number := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseFloat(string(number), 64)
		return parsed, err == nil
	case float64:
		return number, true
	case float32:
		return float64(number), true
	case int:
		return float64(number), true
	case int8:
		return float64(number), true
	case int16:
		return float64(number), true
	case int32:
		return float64(number), true
	case int64:
		return float64(number), true
	case uint:
		return float64(number), true
	case uint8:
		return float64(number), true
	case uint16:
		return float64(number), true
	case uint32:
		return float64(number), true
	case uint64:
		return float64(number), true
	default:
		return 0, false
	}
}

func jsonType(value any) string {
	if value == nil {
		return "null"
	}
	switch value.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "number"
	default:
		return strings.TrimPrefix(fmt.Sprintf("%T", value), "main.")
	}
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}
