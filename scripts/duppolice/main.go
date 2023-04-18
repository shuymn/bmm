package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

const maxConcurrency = 10

var globalRand = rand.New(rand.NewSource(time.Now().UnixNano()))

type Config struct {
	Sources       []string `json:"srcDirs"`
	Destination   string   `json:"destDir"`
	MinDuplicates int      `json:"minDuplicates"`
	Extensions    []string `json:"extensions"`
}

func main() {
	var debug, merge bool
	flag.BoolVar(&debug, "debug", false, "enable debug mode")
	flag.BoolVar(&merge, "merge", false, "enable merge mode")

	flag.Parse()

	config, err := loadConfig()
	if err != nil {
		fmt.Printf("Error loading config: %s", err)
		return
	}

	checksums := make(map[string][]string)
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

			if ok := contains(config.Extensions, filepath.Ext(path)); !ok {
				return nil
			}

			checksum, err := calculateFileChecksum(path)
			if err != nil {
				return err
			}
			checksums[checksum] = append(checksums[checksum], path)
			return nil
		})
		if err != nil {
			fmt.Printf("Error walking directory: %s", err)
			return
		}
	}

	checksums = removeChecksumDuplication(checksums)

	var totalChecksums, totalPaths int
	for checksum, paths := range checksums {
		if len(paths) < config.MinDuplicates {
			continue
		}
		if debug {
			totalChecksums++
			fmt.Println("checksum:", checksum)
			for _, path := range paths {
				totalPaths++
				fmt.Println(" -", path)
			}
			continue
		}
		if err := moveDirectories(config.Destination, checksum, paths); err != nil {
			fmt.Println(err)
			return
		}
	}

	if debug {
		fmt.Println("total checksums:", totalChecksums)
		fmt.Println("total paths:", totalPaths)
		return
	}

	if merge {
		if err = mergeDirectories(config.Destination); err != nil {
			fmt.Println(err)
			return
		}
	}

	if err := removeEmptyDirectory(config.Destination, config.Destination); err != nil {
		fmt.Println(err)
		return
	}
}

func contains(s []string, target string) bool {
	for _, v := range s {
		if v == target {
			return true
		}
	}
	return false
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

	if len(config.Extensions) == 0 {
		return nil, fmt.Errorf("extensions must not be empty")
	}

	if config.MinDuplicates < 1 {
		return nil, fmt.Errorf("minDuplicates must be set to 2 or higher")
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

func calculateFileChecksum(filepath string) (_ string, err error) {
	file, err := os.Open(filepath)
	if err != nil {
		return "", fmt.Errorf("Error opening file: %w", err)
	}
	defer file.Close()

	hash := md5.New()
	_, err = io.Copy(hash, file)
	if err != nil {
		return "", fmt.Errorf("Error copy file: %w", err)
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

func randomString(n int) string {
	var letter = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
	b := make([]rune, n)
	for i := range b {
		b[i] = letter[globalRand.Intn(len(letter))]
	}
	return string(b)
}

func removeChecksumDuplication(checksums map[string][]string) map[string][]string {
	newChecksum := make(map[string][]string)
	for checksum, paths := range checksums {
		dirs := make(map[string]string)
		for _, path := range paths {
			srcDir, _ := filepath.Split(path)
			if _, ok := dirs[srcDir]; !ok {
				dirs[srcDir] = path
			}
		}
		newPaths := make([]string, 0, len(paths))
		for _, path := range dirs {
			newPaths = append(newPaths, path)
		}
		newChecksum[checksum] = newPaths
	}
	return newChecksum
}

func removeEmptyDirectory(destination, path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("Error reading directory: %w", err)
	}
	for _, file := range entries {
		if file.IsDir() {
			subdir := filepath.Join(path, file.Name())
			if err := removeEmptyDirectory(destination, subdir); err != nil {
				return err
			}
		}
	}
	entries, err = os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("Error re-reading directory: %w", err)
	}
	if len(entries) == 0 && path != destination {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("Error removing directory: %w", err)
		}
		fmt.Printf("Removed empty directory: %s\n", path)
	}
	return nil
}

func moveDirectories(dest string, checksum string, paths []string) error {
	destRootDir := filepath.Join(dest, checksum)
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("Error checking file: %w", err)
		}

		if _, err := os.Stat(destRootDir); err != nil {
			if !os.IsNotExist(err) {
				return fmt.Errorf("Error checking file: %w", err)
			}
			if err = os.MkdirAll(destRootDir, 0755); err != nil {
				return fmt.Errorf("Error creating destination directory: %w", err)
			}
		}

		srcDir, _ := filepath.Split(path)
		destDirName := strings.Join([]string{filepath.Base(srcDir), randomString(5)}, "_")
		destDir := filepath.Join(destRootDir, destDirName)

		if err := os.Rename(srcDir, destDir); err != nil {
			return fmt.Errorf("Error moving directory: %w", err)
		}
		fmt.Printf("Successfully moved directory to: %s\n", destDir)
	}
	return nil
}

func mergeDirectories(root string) error {
	subdirs, err := getSubDirectories(root)
	if err != nil {
		return fmt.Errorf("Error getting subdirectories: %w", err)
	}

	var eg errgroup.Group
	semaphore := make(chan struct{}, maxConcurrency)
	for _, subdir := range subdirs {
		subdir := subdir
		semaphore <- struct{}{}

		eg.Go(func() error {
			defer func() { <-semaphore }()

			var destDir string
			srcDirs, err := getSubDirectories(subdir)
			if err != nil {
				return fmt.Errorf("Error getting subsubdirectories: %w", err)
			}
			for _, srcDir := range srcDirs {
				destDir = filepath.Dir(srcDir)
				err := filepath.WalkDir(srcDir, func(srcPath string, d fs.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if d.IsDir() {
						return nil
					}
					relPath, err := filepath.Rel(srcDir, srcPath)
					if err != nil {
						return err
					}
					destPath := filepath.Join(destDir, relPath)
					if err = os.MkdirAll(filepath.Dir(destPath), os.ModePerm); err != nil {
						return err
					}
					if err = copyFile(srcPath, destPath); err != nil {
						return err
					}
					if err = os.Remove(srcPath); err != nil {
						return fmt.Errorf("Error deleting file: %w", err)
					}
					return nil
				})
				if err != nil {
					return fmt.Errorf("Error walking directory: %w", err)
				}
			}
			if destDir != "" {
				fmt.Printf("Successfully merged directory: %s\n", destDir)
			}
			return nil
		})
	}
	return eg.Wait()
}

func copyFile(src, dest string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("Error opening file: %w", err)
	}
	defer srcFile.Close()

	destFile, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("Error creating file: %w", err)
	}
	defer destFile.Close()

	if _, err = io.Copy(destFile, srcFile); err != nil {
		return fmt.Errorf("Error copying file: %w", err)
	}
	return nil
}

func getSubDirectories(root string) ([]string, error) {
	var subdirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if filepath.Dir(path) == root {
			subdirs = append(subdirs, path)
		}
		return filepath.SkipDir
	})
	if err != nil {
		return nil, err
	}
	return subdirs, nil
}
