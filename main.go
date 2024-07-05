package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

func main() {
	rootDir := "."

	var wg sync.WaitGroup

	err := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if strings.HasSuffix(path, ".png") {
			wg.Add(1)
			go convertAndRemoveFile(path, "WebP", convertToWebP, &wg)
		}

		if strings.HasSuffix(path, ".mp3") {
			wg.Add(1)
			go convertAndRemoveFile(path, "Opus", convertToOpus, &wg)
		}

		return nil
	})

	if err != nil {
		fmt.Printf("Error walking the path %s: %v\n", rootDir, err)
	}

	wg.Wait()
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
	err := cmd.Run()

	return err
}

func convertToOpus(mp3Path string) error {
	opusPath := strings.TrimSuffix(mp3Path, filepath.Ext(mp3Path)) + ".opus"
	cmd := exec.Command("ffmpeg", "-i", mp3Path, "-c:a", "libopus", opusPath)
	err := cmd.Run()

	return err
}
