package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	_ "embed"

	"github.com/google/uuid"
	"github.com/saintfish/chardet"
	"golang.org/x/exp/slices"
	"golang.org/x/sync/errgroup"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/unicode/utf32"
	"golang.org/x/text/transform"

	_ "github.com/mattn/go-sqlite3"
)

const (
	dbPath     = "./bms.db"
	configPath = "./config.json"

	maxConcurrency = 10
	upsertBatch    = 1000
	bufferSize     = 128 * 1024
)

var (
	reBMSTitle     = regexp.MustCompile(`(?i)^#title[\s\t]*(.*?)(?:\r\n|\r|\n|$)`)
	reBMSSubtitle  = regexp.MustCompile(`(?i)^#subtitle[\s\t]*(.*?)(?:\r\n|\r|\n|$)`)
	reBMSArtist    = regexp.MustCompile(`(?i)^#artist[\s\t]*(.*?)(?:\r\n|\r|\n|$)`)
	reBMSSubartist = regexp.MustCompile(`(?i)^#subartist[\s\t]*(.*?)(?:\r\n|\r|\n|$)`)
)

//go:embed schema.sql
var schema []byte

func main() {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, err = db.ExecContext(ctx, string(schema)); err != nil {
		fmt.Println(err)
		return
	}

	cli, err := NewCLI(ctx, db)
	if err != nil {
		fmt.Println(err)
		return
	}

	if err := cli.Run(ctx); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("done")
}

type Config struct {
	Sources    []string `json:"srcDirs"`
	Extensions []string `json:"extensions"`
}

func NewConfig() (*Config, error) {
	file, err := os.Open(configPath)
	if err != nil {
		return nil, fmt.Errorf("Error opening file: %w", err)
	}
	defer file.Close()

	b, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("Error reading file: %w", err)
	}

	var config Config
	if err := json.Unmarshal(b, &config); err != nil {
		return nil, fmt.Errorf("Error parsing JSON: %w", err)
	}

	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &config, nil
}

func (c *Config) Validate() error {
	if len(c.Extensions) == 0 {
		return fmt.Errorf("extensions must not be empty")
	}

	if len(c.Sources) == 0 {
		return fmt.Errorf("srcDirs must not be empty")
	}

	for _, src := range c.Sources {
		if !filepath.IsAbs(src) {
			return fmt.Errorf("srcDir (%s) must not be a relative path", src)
		}
		if err := checkDirectoryExistance(src); err != nil {
			return err
		}
	}
	return nil
}

type CLI struct {
	config      *Config
	db          *sql.DB
	bmsList     []*BMS
	songs       map[string]string
	sliceMutex  sync.Mutex
	mapMutex    sync.Mutex
	upsertMutex sync.Mutex
}

func NewCLI(ctx context.Context, db *sql.DB) (*CLI, error) {
	config, err := NewConfig()
	if err != nil {
		return nil, err
	}

	return &CLI{
		config: config,
		db:     db,
	}, nil
}

func (c *CLI) Run(ctx context.Context) error {
	var err error
	c.songs, err = c.ListSongs(ctx)
	if err != nil {
		return err
	}

	var eg errgroup.Group
	semaphore := make(chan struct{}, maxConcurrency)
	c.bmsList = make([]*BMS, 0, upsertBatch)
	for _, root := range c.config.Sources {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if ok := slices.Contains(c.config.Extensions, filepath.Ext(path)); !ok {
				return nil
			}

			semaphore <- struct{}{}
			eg.Go(func() error {
				defer func() { <-semaphore }()
				return c.UpsertPatterns(ctx, path)
			})
			return nil
		})
		if err != nil {
			return err
		}
	}
	return eg.Wait()
}

func (c *CLI) ListSongs(ctx context.Context) (map[string]string, error) {
	rows, err := c.db.QueryContext(ctx, "SELECT id, path FROM songs")
	if err != nil {
		return nil, fmt.Errorf("Error querying database: %w", err)
	}
	defer rows.Close()

	songs := make(map[string]string)
	for rows.Next() {
		var id, path string
		if err = rows.Scan(&id, &path); err != nil {
			return nil, fmt.Errorf("Error scanning row: %w", err)
		}
		songs[path] = id
	}
	return songs, nil
}

func (c *CLI) UpsertPatterns(ctx context.Context, path string) error {
	bms, err := ParseBMS(path)
	if err != nil {
		return err
	}
	c.AppendBMSList(bms)

	if len(c.bmsList) < upsertBatch {
		return nil
	}
	return c.UpsertPattern(ctx)
}

func (c *CLI) UpsertPattern(ctx context.Context) error {
	c.upsertMutex.Lock()
	defer c.upsertMutex.Unlock()

	tx, err := c.db.Begin()
	if err != nil {
		return fmt.Errorf("Error starting transaction: %w", err)
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	stmt1, err := tx.PrepareContext(ctx, `
INSERT INTO patterns (hash, title, subtitle, artist, subartist, path, song_id)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(hash) DO UPDATE SET
	title = excluded.title,
	subtitle = excluded.subtitle,
	artist = excluded.artist,
	subartist = excluded.subartist,
	path = excluded.path,
	song_id = excluded.song_id;`)
	if err != nil {
		return fmt.Errorf("Error preparing statement: %w", err)
	}
	defer stmt1.Close()

	for _, bms := range c.bmsList {
		path := filepath.Dir(bms.Path)
		songID, ok := c.songs[path]
		if !ok {
			stmt2, err := tx.PrepareContext(ctx, "INSERT INTO songs (id, path) VALUES (?, ?)")
			if err != nil {
				return fmt.Errorf("Error preparing statement: %w", err)
			}
			defer stmt2.Close()

			id, err := uuid.NewRandom()
			if err != nil {
				return fmt.Errorf("Error generating UUID: %w", err)
			}
			songID = id.String()
			_, err = stmt2.ExecContext(ctx, songID, path)
			if err != nil {
				return fmt.Errorf("Error executing statement: %w", err)
			}
			c.mapMutex.Lock()
			c.songs[path] = songID
			c.mapMutex.Unlock()
		}
		_, err = stmt1.ExecContext(ctx, bms.Hash, bms.Title, bms.Subtitle, bms.Artist, bms.Subartist, bms.Path, songID)
		if err != nil {
			return fmt.Errorf("Error executing statement: %w", err)
		}
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("Error committing transaction: %w", err)
	}

	c.ResetBMSList()

	return nil
}

func (c *CLI) AppendBMSList(bms *BMS) {
	c.sliceMutex.Lock()
	c.bmsList = append(c.bmsList, bms)
	c.sliceMutex.Unlock()
}

func (c *CLI) ResetBMSList() {
	c.sliceMutex.Lock()
	c.bmsList = c.bmsList[:0]
	c.sliceMutex.Unlock()
}

type BMS struct {
	Path      string
	Hash      string
	Title     string
	Subtitle  string
	Artist    string
	Subartist string
}

func ParseBMS(path string) (*BMS, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("Error opening file: %w", err)
	}
	defer file.Close()

	b, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("Error reading file: %w", err)
	}

	b, err = convertToUTF8(b)
	if err != nil {
		return nil, fmt.Errorf("Error file: %s: %w", path, err)
	}

	bms := &BMS{
		Path: path,
		Hash: calculateHash(b),
	}

	scanner := bufio.NewScanner(bytes.NewReader(b))
	buf := make([]byte, 0, bufferSize)
	scanner.Buffer(buf, bufferSize)
	for scanner.Scan() {
		if bms.Artist != "" && bms.Subartist != "" && bms.Title != "" && bms.Subtitle != "" {
			break
		}
		line := scanner.Text()
		match := reBMSTitle.FindStringSubmatch(line)
		if len(match) > 1 {
			if bms.Title == "" {
				bms.Title = match[1]
			}
			continue
		}
		match = reBMSSubtitle.FindStringSubmatch(line)
		if len(match) > 1 {
			if bms.Subtitle == "" {
				bms.Subtitle = match[1]
			}
			continue
		}
		match = reBMSArtist.FindStringSubmatch(line)
		if len(match) > 1 {
			if bms.Artist == "" {
				bms.Artist = match[1]
			}
			continue
		}
		match = reBMSSubartist.FindStringSubmatch(line)
		if len(match) > 1 {
			if bms.Subartist == "" {
				bms.Subartist = match[1]
			}
			continue
		}
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return nil, fmt.Errorf("Error scanning file: %w", err)
	}

	return bms, nil
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

func calculateHash(input []byte) string {
	hash := sha256.Sum256(input)
	return fmt.Sprintf("%x", hash)
}

func convertToUTF8(input []byte) ([]byte, error) {
	d := chardet.NewTextDetector()
	res, err := d.DetectBest(input)
	if err != nil {
		return nil, fmt.Errorf("Error detecting encoding: %w", err)
	}

	dec := japanese.ShiftJIS.NewDecoder()
	if res.Confidence == 100 {
		switch res.Charset {
		case "UTF-8":
			// No conversion needed
			return input, nil
		case "UTF-32BE":
			dec = utf32.UTF32(utf32.BigEndian, utf32.IgnoreBOM).NewDecoder()
		case "UTF-32LE":
			dec = utf32.UTF32(utf32.LittleEndian, utf32.IgnoreBOM).NewDecoder()
		case "EUC-KR":
			dec = korean.EUCKR.NewDecoder()
		default:
			if res.Charset != "Shift_JIS" {
				fmt.Println("Unknown encoding:", res.Charset)
			}
		}
	}
	output, err := io.ReadAll(transform.NewReader(bytes.NewReader(input), dec))
	if err != nil {
		return nil, fmt.Errorf("Error converting to UTF-8: %w", err)
	}
	return output, nil
}
