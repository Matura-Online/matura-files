package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	ByType    map[string]TypeStats       `json:"byType"`
	BySubject map[string]SubjectStats     `json:"bySubject"`
	Total     TypeStats                   `json:"total"`
}

type SubjectStats struct {
	TotalBytes int64            `json:"totalBytes"`
	Count      int              `json:"count"`
	ByType     map[string]TypeStats `json:"byType"`
	ByYearTerm map[string]SubjectYearStats `json:"byYearTerm"`
}

type SubjectYearStats struct {
	TotalBytes int64            `json:"totalBytes"`
	Count      int              `json:"count"`
	ByType     map[string]TypeStats `json:"byType"`
}

func main() {
	var wg sync.WaitGroup

	imageFormats := []string{".png", ".jpg", ".jpeg"}
	audioFormats := []string{".mp3", ".wav"}

	err := filepath.Walk("source", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		for _, format := range imageFormats {
			if strings.HasSuffix(path, format) {
				wg.Add(1)
				go convertAndRemoveFile(path, "WebP", convertToWebP, &wg)
			}
		}

		for _, format := range audioFormats {
			if strings.HasSuffix(path, format) {
				wg.Add(1)
				go convertAndRemoveFile(path, "Opus", convertToOpus, &wg)
			}
		}

		if strings.HasSuffix(strings.ToLower(path), ".pdf") {
			wg.Add(1)
			go optimizePDFInPlace(path, &wg)
		}

		return nil
	})

	if err != nil {
		fmt.Printf("Error walking the path %s: %v\n", ".", err)
	}

	wg.Wait()

	stats := collectStats("source")

	statsFile, err := os.Create("source/stats.json")
	if err != nil {
		fmt.Printf("Error creating stats.json: %v\n", err)
		return
	}
	defer statsFile.Close()
	enc := json.NewEncoder(statsFile)
	enc.SetIndent("", "  ")
	if err := enc.Encode(stats); err != nil {
		fmt.Printf("Error encoding stats.json: %v\n", err)
	}
	fmt.Printf("Generated stats.json (%d subjects)\n", len(stats.BySubject))

	root, err := buildFileTree("source")
	if err != nil {
		fmt.Printf("Error building file tree: %v\n", err)
		return
	}

	file, err := os.Create("source/files.json")
	if err != nil {
		fmt.Printf("Error creating files.json: %v\n", err)
		return
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(root.Children); err != nil {
		fmt.Printf("Error encoding JSON: %v\n", err)
	}
}

func optimizePDFInPlace(path string, wg *sync.WaitGroup) {
	defer wg.Done()

	if pdfIsAlreadyOptimized(path) {
		return
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".pdf-optimized-*.pdf")
	if err != nil {
		fmt.Printf("Error creating temporary PDF for %s: %v\n", path, err)
		return
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		fmt.Printf("Error closing temporary PDF for %s: %v\n", path, err)
		return
	}
	defer os.Remove(tmpPath)

	cmd := exec.Command("gs", "-sDEVICE=pdfwrite", "-dCompatibilityLevel=1.4", "-dPDFSETTINGS=/ebook", "-dNOPAUSE", "-dQUIET", "-dBATCH", "-sOutputFile="+tmpPath, path)
	if output, err := cmd.CombinedOutput(); err != nil {
		fmt.Printf("Error optimizing PDF %s: %v (%s)\n", path, err, strings.TrimSpace(string(output)))
		return
	}
	if err := os.Rename(tmpPath, path); err != nil {
		fmt.Printf("Error replacing PDF %s: %v\n", path, err)
		return
	}
	fmt.Printf("Optimized %s.\n", path)
}

func pdfIsAlreadyOptimized(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	header := make([]byte, 8)
	n, _ := f.Read(header)
	return n >= 8 && string(header[:8]) == "%PDF-1.4"
}

func convertAndRemoveFile(path, targetFormat string, convertFunc func(string) error, wg *sync.WaitGroup) {
	defer wg.Done()

	err := convertFunc(path)
	if err != nil {
		fmt.Printf("Error converting %s to %s: %v\n", path, targetFormat, err)
		return
	}

	fmt.Printf("Converted %s to %s.\n", path, targetFormat)

	err = os.Remove(path)
	if err != nil {
		fmt.Printf("Error removing %s: %v\n", path, err)
		return
	}

	fmt.Printf("Removed %s.\n", path)
}

func convertToWebP(pngPath string) error {
	webpPath := strings.TrimSuffix(pngPath, filepath.Ext(pngPath)) + ".webp"
	cmd := exec.Command("ffmpeg", "-i", pngPath, webpPath)
	return cmd.Run()
}

func convertToOpus(mp3Path string) error {
	opusPath := strings.TrimSuffix(mp3Path, filepath.Ext(mp3Path)) + ".opus"
	cmd := exec.Command("ffmpeg", "-i", mp3Path, "-c:a", "libopus", opusPath)
	return cmd.Run()
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

		if len(parts) >= 1 && parts[0] == "All" {
			return nil
		}
		if len(parts) >= 1 && (parts[0] == "Raw" || parts[0] == "Json") {
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
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || path == root {
			return err
		}

		relPath, err := filepath.Rel(root, path)
		if err != nil || relPath == "." {
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
				newNode := FileNode{
					Name:  part,
					IsDir: d.IsDir(),
				}
				curr.Children = append(curr.Children, newNode)
				curr = &curr.Children[len(curr.Children)-1]
			}

			if i == len(parts)-1 && !d.IsDir() {
				info, _ := d.Info()
				if info != nil {
					curr.Size = info.Size()
				}
				break
			}
		}

		return nil
	})

	return rootNode, err
}
