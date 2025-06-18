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
	Children []FileNode `json:"children,omitempty"`
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

		return nil
	})

	if err != nil {
		fmt.Printf("Error walking the path %s: %v\n", ".", err)
	}

	wg.Wait()

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

func buildFileTree(root string) (FileNode, error) {
	rootNode := FileNode{Name: filepath.Base(root), IsDir: true}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || path == "." {
			return err
		}

		relPath, _ := filepath.Rel(root, path)
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
				break
			}
		}

		return nil
	})

	return rootNode, err
}
