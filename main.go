package main

import (
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
