package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

type Config struct {
	Sources     []string `json:"srcDirs"`
	Destination string   `json:"destDir"`
}

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "enable debug mode")

	flag.Parse()

	config, err := loadConfig()
	if err != nil {
		fmt.Printf("Error loading config.json: %s", err)
		return
	}

	corrupted, err := loadCorrupted()
	if err != nil {
		fmt.Printf("Error loading corrupted.json: %s", err)
		return
	}

	dirs := make([]string, 0, 10000)
	for _, root := range config.Sources {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == config.Destination {
					return filepath.SkipDir
				}
				return nil
			}

			if ok := contains(corrupted, filepath.Base(path)); ok {
				dirs = append(dirs, filepath.Dir(path))
				return filepath.SkipDir
			}
			return nil
		})
		if err != nil {
			fmt.Printf("Error walking directory: %s", err)
			return
		}
	}

	if debug {
		for _, dir := range dirs {
			fmt.Println(dir)
		}
	}

	if err = moveDirectories(config.Destination, dirs); err != nil {
		fmt.Printf("Error moving directories: %s", err)
		return
	}
}

func loadConfig() (config *Config, err error) {
	file, err := os.Open("config.json")
	if err != nil {
		return nil, fmt.Errorf("Error opening file: %w", err)
	}
	defer file.Close()

	b, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("Error reading file: %w", err)
	}

	if err := json.Unmarshal(b, &config); err != nil {
		return nil, fmt.Errorf("Error parsing JSON: %w", err)
	}

	if len(config.Sources) == 0 {
		return nil, fmt.Errorf("srcDirs must not be empty")
	}
	if config.Destination == "" {
		return nil, fmt.Errorf("destDir must not be empty")
	}

	if !filepath.IsAbs(config.Destination) {
		return nil, fmt.Errorf("destDir (%s) must not be a relative path", config.Destination)
	}

	if err = checkDirectoryExistance(config.Destination); err != nil {
		return nil, err
	}

	for _, src := range config.Sources {
		if !filepath.IsAbs(src) {
			return nil, fmt.Errorf("srcDir (%s) must not be a relative path", src)
		}
		if isSubdirectory(src, config.Destination) {
			return nil, fmt.Errorf("destDir (%s) must not be a subdirectory of any srcDirs", config.Destination)
		}
		if err = checkDirectoryExistance(src); err != nil {
			return nil, err
		}
	}

	return config, nil
}

func isSubdirectory(parent, child string) bool {
	parent = filepath.Clean(parent) + string(os.PathSeparator)
	child = filepath.Clean(child)

	return len(child) > len(parent) && child[:len(parent)] == parent
}

func checkDirectoryExistance(path string) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("directory does not exist: %s", path)
		}
		return fmt.Errorf("Error checking directory: %w", err)
	}
	return nil
}

func loadCorrupted() ([]string, error) {
	file, err := os.Open("corrupted.json")
	if err != nil {
		return nil, fmt.Errorf("Error opening file: %w", err)
	}
	defer file.Close()

	b, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("Error reading file: %w", err)
	}

	corrupted := make([]string, 0, 50000)
	if err := json.Unmarshal(b, &corrupted); err != nil {
		return nil, fmt.Errorf("Error parsing JSON: %w", err)
	}
	return corrupted, nil
}

func contains(s []string, target string) bool {
	for _, v := range s {
		if v == target {
			return true
		}
	}
	return false
}

func moveDirectories(dest string, srcDirs []string) error {
	for _, srcDir := range srcDirs {
		if _, err := os.Stat(srcDir); err != nil {
			return fmt.Errorf("Error checking directory: %w", err)
		}

		destDir := filepath.Join(dest, filepath.Base(srcDir))
		_, err := os.Stat(destDir)
		if err == nil {
			return fmt.Errorf("Destination directory already exists: %s", destDir)
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("Error checking directory: %w", err)
		}

		if err := os.Rename(srcDir, destDir); err != nil {
			return fmt.Errorf("Error moving directory: %w", err)
		}
		fmt.Printf("Successfully moved directory to: %s\n", destDir)
	}
	return nil
}
