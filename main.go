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

type FileNode struct {
	Name     string     `json:"name"`
	IsDir    bool       `json:"isDir"`
	Size     int64      `json:"size,omitempty"`
	Children []FileNode `json:"children,omitempty"`
}

type TypeStats struct {
	TotalBytes int64 `json:"totalBytes"`
	Count      int   `json:"count"`
}

type Stats struct {
	ByType    map[string]TypeStats    `json:"byType"`
	BySubject map[string]SubjectStats `json:"bySubject"`
	Total     TypeStats               `json:"total"`
}

type SubjectStats struct {
	TotalBytes int64                       `json:"totalBytes"`
	Count      int                         `json:"count"`
	ByType     map[string]TypeStats        `json:"byType"`
	ByYearTerm map[string]SubjectYearStats `json:"byYearTerm"`
}

type SubjectYearStats struct {
	TotalBytes int64                `json:"totalBytes"`
	Count      int                  `json:"count"`
	ByType     map[string]TypeStats `json:"byType"`
}

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
	flag.Parse()
	if *refreshPrograms {
		if err := refreshStudyPrograms(filepath.Join("source", "programi.json")); err != nil {
			fmt.Fprintf(os.Stderr, "study refresh failed: %v\n", err)
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

	stats := collectStats("source")
	statsFile, _ := os.Create("source/stats.json")
	defer statsFile.Close()
	enc := json.NewEncoder(statsFile)
	enc.SetIndent("", "  ")
	enc.Encode(stats)
	fmt.Printf("Generated stats.json (%d subjects)\n", len(stats.BySubject))

	root, _ := buildFileTree("source")
	file, _ := os.Create("source/files.json")
	defer file.Close()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	encoder.Encode(root.Children)
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

func ext(path string) string {
	s := strings.ToLower(filepath.Ext(path))
	if s == "" {
		return s
	}
	return s[1:]
}

func collectStats(root string) Stats {
	stats := Stats{
		ByType:    map[string]TypeStats{},
		BySubject: map[string]SubjectStats{},
	}

	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}

		rel, _ := filepath.Rel(root, path)
		parts := strings.Split(rel, string(filepath.Separator))
		t := ext(path)
		sz := info.Size()

		ts := stats.ByType[t]
		ts.TotalBytes += sz
		ts.Count++
		stats.ByType[t] = ts
		stats.Total.TotalBytes += sz
		stats.Total.Count++

		if len(parts) >= 1 && (parts[0] == "All" || parts[0] == "Raw" || parts[0] == "Json") {
			return nil
		}

		if len(parts) >= 3 {
			subj := parts[2]
			ss := stats.BySubject[subj]
			if ss.ByType == nil {
				ss.ByType = map[string]TypeStats{}
				ss.ByYearTerm = map[string]SubjectYearStats{}
			}
			ss.TotalBytes += sz
			ss.Count++

			st := ss.ByType[t]
			st.TotalBytes += sz
			st.Count++
			ss.ByType[t] = st

			if len(parts) >= 4 {
				ytKey := parts[0] + "/" + parts[1] + "/" + parts[3]
				sys := ss.ByYearTerm[ytKey]
				if sys.ByType == nil {
					sys.ByType = map[string]TypeStats{}
				}
				sys.TotalBytes += sz
				sys.Count++
				sty := sys.ByType[t]
				sty.TotalBytes += sz
				sty.Count++
				sys.ByType[t] = sty
				ss.ByYearTerm[ytKey] = sys
			}

			stats.BySubject[subj] = ss
		}

		return nil
	})

	return stats
}

func buildFileTree(root string) (FileNode, error) {
	rootNode := FileNode{Name: filepath.Base(root), IsDir: true, Size: 0}

	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || path == root {
			return err
		}

		relPath, _ := filepath.Rel(root, path)
		if relPath == "." {
			return nil
		}
		parts := strings.Split(relPath, string(filepath.Separator))
		curr := &rootNode

		for i, part := range parts {
			if part == "" || part == "." {
				continue
			}

			found := false
			for j := range curr.Children {
				if curr.Children[j].Name == part {
					curr = &curr.Children[j]
					found = true
					break
				}
			}

			if !found {
				newNode := FileNode{Name: part, IsDir: d.IsDir()}
				curr.Children = append(curr.Children, newNode)
				curr = &curr.Children[len(curr.Children)-1]
			}

			if i == len(parts)-1 && !d.IsDir() {
				if info, _ := d.Info(); info != nil {
					curr.Size = info.Size()
				}
				break
			}
		}

		return nil
	})

	return rootNode, nil
}
