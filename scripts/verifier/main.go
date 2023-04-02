package main

import (
	"bufio"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"golang.org/x/text/encoding/japanese"
)

const (
	extWAV = ".wav"
	extOGG = ".ogg"

	lr2irBaseURL = "http://www.dream-pro.info/~lavalse/LR2IR/search.cgi"
)

var wavRegexp = regexp.MustCompile(`#WAV([0-9A-Za-z]{2})\s(.*)`)

type Config struct {
	Sources    []string `json:"srcDirs"`
	Extensions []string `json:"extensions"`
	Ignore     []string `json:"ignore"`
}

func main() {
	config, err := loadConfig()
	if err != nil {
		fmt.Printf("Error loading config: %s", err)
		return
	}

	for _, src := range config.Sources {
		err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			idx := containsFunc(config.Ignore, func(s string) bool {
				return strings.HasPrefix(path, s)
			})
			if idx != -1 {
				return nil
			}
			if idx = contains(config.Extensions, filepath.Ext(path)); idx == -1 {
				return nil
			}
			wavs, err := getWAVs(path)
			if err != nil {
				return err
			}
			if len(wavs) == 0 {
				return nil
			}
			parentPath := filepath.Dir(path)
			notFoundWAVs := make([]string, 0, len(wavs))
			for _, wav := range wavs {
				if runtime.GOOS == "windows" {
					wav, err = japanese.ShiftJIS.NewDecoder().String(wav)
					if err != nil {
						return fmt.Errorf("Error decoding string: %w", err)
					}
				}
				_, err := os.Stat(filepath.Join(parentPath, wav))
				if err == nil {
					continue
				}
				if !os.IsNotExist(err) {
					return fmt.Errorf("Error checking file: %w", err)
				}
				ext := filepath.Ext(wav)
				var newWAV string
				switch ext {
				case extWAV:
					newWAV = wav[:len(wav)-len(ext)] + extOGG
				case extOGG:
					newWAV = wav[:len(wav)-len(ext)] + extWAV
				}
				_, err = os.Stat(filepath.Join(parentPath, newWAV))
				if err == nil {
					continue
				}
				if !os.IsNotExist(err) {
					return fmt.Errorf("Error checking file: %w", err)
				}
				notFoundWAVs = append(notFoundWAVs, wav)
			}
			if len(notFoundWAVs) == 0 {
				return nil
			}
			checksum, err := calculateFileChecksum(path)
			if err != nil {
				return fmt.Errorf("Error calculating checksum: %w", err)
			}
			u := getIR2IRURL(checksum)
			fmt.Printf(
				"Missing WAVs in %s:\n - URL: %s\n - total\t%d\n - missing\t%d (%.1f%%)\n",
				path,
				u,
				len(wavs),
				len(notFoundWAVs),
				float64(len(notFoundWAVs))/float64(len(wavs))*100,
			)
			for i, wav := range notFoundWAVs {
				if i > 10 {
					fmt.Printf("   - ...\n")
					break
				}
				fmt.Printf("   - %s\n", wav)
			}
			fmt.Printf("\n")
			return nil
		})
		if err != nil {
			fmt.Printf("Error walking directory: %s", err)
			return
		}
	}
}

func loadConfig() (*Config, error) {
	file, err := os.Open("config.json")
	if err != nil {
		return nil, fmt.Errorf("Error opening file: %w", err)
	}
	defer file.Close()

	b, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("Error reading file: %w", err)
	}

	var config *Config
	if err := json.Unmarshal(b, &config); err != nil {
		return nil, fmt.Errorf("Error parsing JSON: %w", err)
	}

	if len(config.Extensions) == 0 {
		return nil, fmt.Errorf("extensions must not be empty")
	}

	if len(config.Sources) == 0 {
		return nil, fmt.Errorf("srcDirs must not be empty")
	}

	for _, src := range config.Sources {
		if !filepath.IsAbs(src) {
			return nil, fmt.Errorf("source directory (%s) must not be a relative path", src)
		}
		if err = checkDirectoryExistance(src); err != nil {
			return nil, err
		}
	}

	return config, nil
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

func contains(s []string, target string) int {
	for i, v := range s {
		if v == target {
			return i
		}
	}
	return -1
}

func containsFunc(s []string, f func(string) bool) int {
	for i, v := range s {
		if f(v) {
			return i
		}
	}
	return -1
}

func getWAVs(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("Error opening file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	wavs := make([]string, 0, 100)
	for scanner.Scan() {
		line := scanner.Text()
		matches := wavRegexp.FindStringSubmatch(line)
		if len(matches) > 0 {
			file := matches[2]
			if strings.HasSuffix(file, extWAV) || strings.HasSuffix(file, extOGG) {
				wavs = append(wavs, file)
			}
		}
	}

	return wavs, nil
}

func getIR2IRURL(bmsmd5 string) string {
	u, err := url.Parse(lr2irBaseURL)
	if err != nil {
		panic(err)
	}
	q := u.Query()
	q.Set("bmsmd5", bmsmd5)
	u.RawQuery = q.Encode()
	return u.String()
}

func calculateFileChecksum(path string) (string, error) {
	file, err := os.Open(path)
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
