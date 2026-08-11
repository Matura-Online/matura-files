package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

func cleanStaleTemps(root string) {
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if strings.HasPrefix(base, ".opus-recompress-") || strings.HasPrefix(base, ".pdf-optimized-") {
			os.Remove(path)
			fmt.Printf("Cleaned stale temp: %s\n", path)
		}
		return nil
	})
}

func main() {
	refreshPrograms := flag.Bool("refresh-programs", false, "download and parse the live study catalog")
	studyHTMLArchive := flag.String("study-html-archive", "", "write fetched study HTML files to one ZIP archive")
	studyHTMLDir := flag.String("parse-study-html", "", "parse an existing directory of study HTML files")
	studyCatalog := flag.String("study-catalog", "", "catalog JSON for --parse-study-html (defaults to ../source/programi.json)")
	studyOutput := flag.String("study-output", filepath.Join("source", "programi.json"), "output JSON path for study parsing")
	studyFiltersOutput := flag.String("study-filters-output", filepath.Join("source", "filters.json"), "output JSON path for dependent study filters")
	studyValidation := flag.String("validate-study", "", "validate an existing programi.json against the publication schema")
	flag.Parse()
	if *studyValidation != "" {
		if *refreshPrograms || *studyHTMLDir != "" {
			fmt.Fprintln(os.Stderr, "study validation cannot be combined with a study refresh or HTML parse")
			os.Exit(2)
		}
		if err := validateStudyCatalogFile(*studyValidation); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Study catalog schema valid: %s\n", *studyValidation)
		return
	}
	if *refreshPrograms && *studyHTMLDir != "" {
		fmt.Fprintln(os.Stderr, "study refresh failed: --refresh-programs and --parse-study-html are mutually exclusive")
		os.Exit(2)
	}
	if *refreshPrograms {
		if err := refreshStudyPrograms(*studyOutput, *studyFiltersOutput, *studyHTMLArchive); err != nil {
			fmt.Fprintf(os.Stderr, "study refresh failed: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if *studyHTMLDir != "" {
		catalogPath := *studyCatalog
		if catalogPath == "" {
			catalogPath = filepath.Join(filepath.Dir(*studyHTMLDir), "source", "programi.json")
		}
		if err := parseStudyProgramsFromHTML(*studyHTMLDir, catalogPath, *studyOutput); err != nil {
			fmt.Fprintf(os.Stderr, "study HTML parse failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	cleanStaleTemps("source")

	var wg sync.WaitGroup
	workers := min(max(runtime.NumCPU()-1, 1), 8)
	sem := make(chan struct{}, workers)
	fmt.Printf("Using %d workers (%d cores available)\n", workers, runtime.NumCPU())

	imageFormats := []string{".png", ".jpg", ".jpeg"}
	audioFormats := []string{".mp3", ".wav"}

	filepath.Walk("source", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		for _, format := range imageFormats {
			if strings.HasSuffix(path, format) {
				wg.Add(1)
				sem <- struct{}{}
				go func(p string) {
					defer wg.Done()
					defer func() { <-sem }()
					convertAndRemoveFile(p, "WebP", convertToWebP)
				}(path)
			}
		}

		for _, format := range audioFormats {
			if strings.HasSuffix(path, format) {
				wg.Add(1)
				sem <- struct{}{}
				go func(p string) {
					defer wg.Done()
					defer func() { <-sem }()
					convertAndRemoveFile(p, "Opus", convertToOpus)
				}(path)
			}
		}

		if strings.HasSuffix(strings.ToLower(path), ".pdf") {
			wg.Add(1)
			sem <- struct{}{}
			go func(p string) { defer wg.Done(); defer func() { <-sem }(); optimizePDFInPlace(p) }(path)
		}

		if strings.HasSuffix(strings.ToLower(path), ".opus") {
			wg.Add(1)
			sem <- struct{}{}
			go func(p string) { defer wg.Done(); defer func() { <-sem }(); recompressOpusInPlace(p) }(path)
		}

		return nil
	})

	wg.Wait()

	if err := writeFilesManifest("source"); err != nil {
		fmt.Fprintf(os.Stderr, "failed to generate files.json: %v\n", err)
		os.Exit(1)
	}

}

type filesManifestEntry struct {
	Name     string               `json:"name"`
	IsDir    bool                 `json:"isDir"`
	Children []filesManifestEntry `json:"children,omitempty"`
	Size     int64                `json:"size,omitempty"`
}

func writeFilesManifest(sourceRoot string) error {
	entries, err := readFilesManifestDirectory(sourceRoot)
	if err != nil {
		return err
	}

	encoded, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')

	manifestPath := filepath.Join(sourceRoot, "files.json")
	if err := os.WriteFile(manifestPath, encoded, 0644); err != nil {
		return err
	}

	fmt.Printf("Generated file manifest: %s\n", manifestPath)
	return nil
}

func readFilesManifestDirectory(directory string) ([]filesManifestEntry, error) {
	directoryEntries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}

	entries := make([]filesManifestEntry, 0, len(directoryEntries))
	for _, directoryEntry := range directoryEntries {
		name := directoryEntry.Name()
		if shouldSkipFilesManifestEntry(name) {
			continue
		}

		info, err := directoryEntry.Info()
		if err != nil {
			return nil, err
		}

		entry := filesManifestEntry{Name: name, IsDir: directoryEntry.IsDir()}
		if entry.IsDir {
			entry.Children, err = readFilesManifestDirectory(filepath.Join(directory, name))
			if err != nil {
				return nil, err
			}
		} else {
			entry.Size = info.Size()
		}

		entries = append(entries, entry)
	}

	return entries, nil
}

func shouldSkipFilesManifestEntry(name string) bool {
	return name == "files.json" || name == "programi.json" || name == "filters.json" || strings.HasPrefix(name, ".")
}

func convertAndRemoveFile(path, targetFormat string, convertFunc func(string) error) {
	if err := convertFunc(path); err != nil {
		fmt.Printf("Error converting %s to %s: %v\n", path, targetFormat, err)
		return
	}
	fmt.Printf("Converted %s to %s.\n", path, targetFormat)
	if err := os.Remove(path); err != nil {
		fmt.Printf("Error removing %s: %v\n", path, err)
	}
}

func convertToWebP(pngPath string) error {
	webpPath := strings.TrimSuffix(pngPath, filepath.Ext(pngPath)) + ".webp"
	return exec.Command("ffmpeg", "-i", pngPath, webpPath).Run()
}

func convertToOpus(mp3Path string) error {
	opusPath := strings.TrimSuffix(mp3Path, filepath.Ext(mp3Path)) + ".opus"
	return exec.Command("nice", "-n", "12", "ffmpeg",
		"-i", mp3Path, "-c:a", "libopus", "-b:a", "32k", "-application", "voip",
		"-y", "-loglevel", "error", "-threads", "1", opusPath,
	).Run()
}

func recompressOpusInPlace(path string) {
	if opusAlreadyDone(path) {
		return
	}

	before := int64(0)
	if fi, _ := os.Stat(path); fi != nil {
		before = fi.Size()
	}
	if before == 0 {
		return
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".opus-recompress-*.opus")
	if err != nil {
		fmt.Printf("Error creating temp for %s: %v\n", path, err)
		return
	}
	tmpPath := tmp.Name()
	tmp.Close()

	cmd := exec.Command("nice", "-n", "12", "ffmpeg",
		"-i", path, "-c:a", "libopus", "-b:a", "32k", "-application", "voip",
		"-y", "-loglevel", "error", "-threads", "1", tmpPath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		fmt.Printf("Error recompressing %s: %v (%s)\n", path, err, strings.TrimSpace(string(output)))
		os.Remove(tmpPath)
		return
	}
	if err := os.Rename(tmpPath, path); err != nil {
		fmt.Printf("Error replacing %s: %v\n", path, err)
		os.Remove(tmpPath)
		return
	}
	after := int64(0)
	if fi, _ := os.Stat(path); fi != nil {
		after = fi.Size()
	}
	pct := 0.0
	if before > 0 {
		pct = float64(after) / float64(before) * 100
	}
	fmt.Printf("Opus: %s  %d -> %d  (%.0f%%)\n", path, before, after, pct)
}

func opusAlreadyDone(path string) bool {
	probe := exec.Command("ffprobe", "-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path,
	)
	out, err := probe.Output()
	if err != nil {
		return false
	}
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	dur := 0.0
	fmt.Sscanf(strings.TrimSpace(string(out)), "%f", &dur)
	if dur <= 0 {
		return false
	}
	bps := float64(fi.Size()) * 8 / dur
	return bps <= 45000
}

func optimizePDFInPlace(path string) {
	if pdfIsAlreadyOptimized(path) {
		return
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".pdf-optimized-*.pdf")
	if err != nil {
		fmt.Printf("Error creating temp PDF for %s: %v\n", path, err)
		return
	}
	tmpPath := tmp.Name()
	tmp.Close()

	cmd := exec.Command("gs", "-sDEVICE=pdfwrite", "-dCompatibilityLevel=1.4", "-dPDFSETTINGS=/ebook",
		"-dNOPAUSE", "-dQUIET", "-dBATCH",
		"-sOutputFile="+tmpPath, "-c", "[ /Title (matura-op-v1) /DOCINFO pdfmark", "-f", path,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		fmt.Printf("Error optimizing PDF %s: %v (%s)\n", path, err, strings.TrimSpace(string(output)))
		os.Remove(tmpPath)
		return
	}
	if err := os.Rename(tmpPath, path); err != nil {
		fmt.Printf("Error replacing PDF %s: %v\n", path, err)
		os.Remove(tmpPath)
		return
	}
	fmt.Printf("Optimized %s.\n", path)
}

func pdfIsAlreadyOptimized(path string) bool {
	cmd := exec.Command("exiftool", "-s", "-s", "-s", "-Title", path)
	out, err := cmd.Output()
	return err == nil && strings.HasPrefix(strings.TrimSpace(string(out)), "matura-op")
}
