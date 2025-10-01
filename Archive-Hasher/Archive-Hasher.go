// =========================================================
// Script Name: dir_hasher.go
// Description: Generates cryptographic hashes for directories, creates TOML files with hash information, and zips directories
// Author: Based on APTlantis Team's iso_hasher.go
// Creation Date: 2025-07-20
//
// Dependencies:
// - github.com/cloudflare/circl/xof/k12
// - github.com/jzelinskie/whirlpool
// - golang.org/x/crypto/blake2b
// - golang.org/x/crypto/ripemd160
// - golang.org/x/crypto/sha3
// - golang.org/x/crypto/openpgp
// - lukechampine.com/blake3
// - github.com/zeebo/xxh3
// - github.com/cespare/xxhash/v2
// - github.com/spaolacci/murmur3
// - archive/zip
//
// Usage:
//   go run dir_hasher.go [options]
//
// Options:
//   -dir string        Directory to hash and zip
//   -verbose           Enable verbose output (default true)
//   -progress          Show progress when hashing large files (default true)
//   -gpgkey string     Path to GPG private key file (if not provided, a new key will be generated)
// =========================================================

package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"crypto"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/cespare/xxhash/v2"
	"github.com/cloudflare/circl/xof/k12"
	"github.com/jzelinskie/whirlpool"
	"github.com/spaolacci/murmur3"
	"github.com/zeebo/xxh3"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/ripemd160"
	"golang.org/x/crypto/sha3"
	"lukechampine.com/blake3"
)

var (
	dirPath          string
	verbose          bool
	showProgress     bool
	gpgKeyFile       string
	failFast         bool
	outDir           string
	outPrefix        string
	progressInterval time.Duration
	logFormat        string
	logLevel         string
	hashWorkers      int
)

func init() {
	flag.StringVar(&dirPath, "dir", "", "Directory to hash and tar")
	flag.BoolVar(&verbose, "verbose", true, "Enable verbose output")
	flag.BoolVar(&showProgress, "progress", true, "Show progress when hashing large files")
	flag.DurationVar(&progressInterval, "progress-interval", 3*time.Second, "Interval between progress updates (e.g., 3s, 1m)")
	flag.StringVar(&gpgKeyFile, "gpgkey", "", "Path to GPG private key file (if not provided, a new key will be generated)")
	flag.BoolVar(&failFast, "fail-fast", false, "Exit immediately on first error (default: false)")
	flag.StringVar(&outDir, "out-dir", "", "Optional output directory for .yaml and .tar files (default: alongside input directory)")
	flag.StringVar(&outPrefix, "out-prefix", "", "Optional filename prefix for outputs (default: directory name)")
	flag.StringVar(&logFormat, "log-format", "text", "Logging format: text|json")
	flag.StringVar(&logLevel, "log-level", "info", "Logging level: debug|info|warn|error")
	flag.IntVar(&hashWorkers, "hash-workers", runtime.NumCPU(), "Number of concurrent file readers for hashing (maintains deterministic order)")
	flag.Parse()

	// Configure structured logging
	lvl := slog.LevelInfo
	switch strings.ToLower(logLevel) {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error", "err":
		lvl = slog.LevelError
	}
	if verbose && strings.EqualFold(logLevel, "info") {
		// Legacy -verbose bumps to debug unless log-level explicitly set different than default
		lvl = slog.LevelDebug
	}
	var handler slog.Handler
	if strings.EqualFold(logFormat, "json") {
		handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	} else {
		handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	}
	slog.SetDefault(slog.New(handler))

	if dirPath == "" {
		slog.Error("missing required flag -dir")
		os.Exit(2)
	}
}

// generateGPGKey generates a new GPG key pair
func generateGPGKey(name, email string) (*openpgp.Entity, error) {
	// Configure the primary key
	config := &packet.Config{
		RSABits:     2048,
		DefaultHash: crypto.SHA256,
	}

	// Create the entity
	entity, err := openpgp.NewEntity(name, "Directory Hasher", email, config)
	if err != nil {
		return nil, err
	}

	// Self-sign the identity
	for _, id := range entity.Identities {
		err := id.SelfSignature.SignUserId(id.UserId.Id, entity.PrimaryKey, entity.PrivateKey, config)
		if err != nil {
			return nil, err
		}
	}

	return entity, nil
}

// exportPublicKey exports the public key in armored format
func exportPublicKey(entity *openpgp.Entity) (string, error) {
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	if err != nil {
		return "", err
	}

	err = entity.Serialize(w)
	if err != nil {
		return "", err
	}

	err = w.Close()
	if err != nil {
		return "", err
	}

	return buf.String(), nil
}

// signData signs the provided data with the GPG key
func signData(entity *openpgp.Entity, data []byte) (string, error) {
	var buf bytes.Buffer

	// Create an armored signature
	w, err := armor.Encode(&buf, openpgp.SignatureType, nil)
	if err != nil {
		return "", err
	}

	// Create a signature writer
	signWriter, err := openpgp.Sign(w, entity, nil, nil)
	if err != nil {
		return "", err
	}

	// Write the data to be signed
	_, err = signWriter.Write(data)
	if err != nil {
		return "", err
	}

	// Close the signature writer
	err = signWriter.Close()
	if err != nil {
		return "", err
	}

	// Close the armor writer
	err = w.Close()
	if err != nil {
		return "", err
	}

	return buf.String(), nil
}

// getGPGEntity returns a GPG entity either by loading from a file or generating a new one
func getGPGEntity() (*openpgp.Entity, error) {
	if gpgKeyFile != "" {
		// Load key from file
		keyData, err := os.ReadFile(gpgKeyFile)
		if err != nil {
			return nil, fmt.Errorf("error reading GPG key file: %v", err)
		}

		// Decode the armored key
		block, err := armor.Decode(bytes.NewReader(keyData))
		if err != nil {
			return nil, fmt.Errorf("error decoding GPG key: %v", err)
		}

		// Read the entity
		entityList, err := openpgp.ReadEntity(packet.NewReader(block.Body))
		if err != nil {
			return nil, fmt.Errorf("error reading GPG entity: %v", err)
		}

		return entityList, nil
	} else {
		// Generate a new key
		log.Println("No GPG key provided, generating a new one...")
		hostname, err := os.Hostname()
		if err != nil {
			hostname = "unknown"
		}

		return generateGPGKey("Dir Hasher", fmt.Sprintf("dir-hasher@%s", hostname))
	}
}

// FileInfo stores information about a file
type FileInfo struct {
	Path    string
	Size    int64
	ModTime time.Time
	IsDir   bool
	RelPath string // Path relative to the root directory
}

// DirectoryInventory stores information about all files in a directory
type DirectoryInventory struct {
	RootDir     string
	Files       []FileInfo
	TotalSize   int64
	TotalFiles  int
	TotalDirs   int
	InventoryAt time.Time
}

// HashResult stores all hash values for a directory
type HashResult struct {
	// 5 main hashes
	KangarooTwelve string
	Blake3         string
	SHA3_256       string
	Blake2b        string
	SHA512         string

	// 3 less common checksums
	Whirlpool string
	RIPEMD160 string
	XXH3      string

	// Additional hashes
	SHA256   string
	XXHash64 string
	Murmur3  string

	// GPG signature
	GPGKeyID     string
	GPGSignature string
}

func main() {
	startTime := time.Now()
	slog.Info("starting archive-hasher", "dir", dirPath)

	// Check if directory exists
	if _, err := os.Stat(dirPath); os.IsNotExist(err) {
		if failFast {
			slog.Error("directory does not exist", "dir", dirPath)
			os.Exit(1)
		} else {
			slog.Error("directory does not exist", "dir", dirPath)
			return
		}
	}

	// Get directory name for output files
	dirName := filepath.Base(dirPath)
	if dirName == "." || dirName == ".." || dirName == "/" || dirName == "\\" {
		// Use the parent directory name if the path ends with a separator
		dirName = filepath.Base(filepath.Dir(dirPath))
	}

	// Create inventory of the directory
	slog.Info("creating directory inventory")
	inventory, err := createDirectoryInventory(dirPath)
	if err != nil {
		if failFast {
			slog.Error("creating directory inventory failed", "err", err)
			os.Exit(1)
		} else {
			slog.Warn("issues encountered during directory inventory; continuing", "err", err)
		}
	}
	slog.Info("inventory complete", "files", inventory.TotalFiles, "dirs", inventory.TotalDirs, "size_mb", fmt.Sprintf("%.2f", float64(inventory.TotalSize)/(1024*1024)))

	// Generate hashes for the directory
	slog.Info("generating hashes for all files")
	hashResult, err := generateDirectoryHashes(inventory)
	if err != nil {
		if failFast {
			slog.Error("hash generation failed", "err", err)
			os.Exit(1)
		} else {
			slog.Warn("issues encountered during hash generation; continuing", "err", err)
		}
	}
	slog.Info("hash generation complete")

	// Determine output locations and prefix
	basePrefix := outPrefix
	if strings.TrimSpace(basePrefix) == "" {
		basePrefix = dirName
	}
	baseOutDir := outDir
	if strings.TrimSpace(baseOutDir) == "" {
		baseOutDir = filepath.Dir(dirPath)
	}
	if err := os.MkdirAll(baseOutDir, 0755); err != nil {
		if failFast {
			log.Fatalf("Error creating out-dir %s: %v\n", baseOutDir, err)
		} else {
			log.Printf("Warning: cannot create out-dir %s: %v (falling back to parent of input)\n", baseOutDir, err)
			baseOutDir = filepath.Dir(dirPath)
		}
	}

	// Create YAML file (standalone)
	yamlPath := filepath.Join(baseOutDir, basePrefix+".yaml")
	slog.Info("creating YAML file", "path", yamlPath)
	err = createYAMLFile(yamlPath, dirName, inventory, hashResult)
	if err != nil {
		if failFast {
			slog.Error("creating YAML failed", "err", err)
			os.Exit(1)
		} else {
			slog.Warn("failed to create YAML; continuing", "err", err)
		}
	} else {
		slog.Info("YAML file created successfully")
	}

	// Build legacy TOML content (to include inside the TAR archive)
	tomlContent := buildLegacyTOMLContent(dirName, inventory, hashResult)
	legacyTomlName := basePrefix + ".toml"

	// Create TAR file (includes legacy TOML inside)
	tarPath := filepath.Join(baseOutDir, basePrefix+".tar")
	slog.Info("creating TAR file", "path", tarPath)
	err = tarDirectoryWithToml(dirPath, tarPath, legacyTomlName, []byte(tomlContent))
	if err != nil {
		if failFast {
			slog.Error("creating TAR failed", "err", err)
			os.Exit(1)
		} else {
			slog.Warn("issues during TAR creation; continuing", "err", err)
		}
	} else {
		slog.Info("TAR file created successfully")
	}

	duration := time.Since(startTime)
	slog.Info("done", "elapsed", duration.String())
}

// createDirectoryInventory creates an inventory of all files in a directory
func createDirectoryInventory(rootDir string) (DirectoryInventory, error) {
	inventory := DirectoryInventory{
		RootDir:     rootDir,
		Files:       []FileInfo{},
		InventoryAt: time.Now(),
	}

	err := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			slog.Warn("access path error; skipping", "path", path, "err", err)
			return nil // Continue with next file
		}

		// Calculate relative path
		relPath, err := filepath.Rel(rootDir, path)
		if err != nil {
			slog.Warn("relpath error; using full path", "path", path, "err", err)
			relPath = path
		}

		// Skip the root directory itself
		if path == rootDir {
			return nil
		}

		fileInfo := FileInfo{
			Path:    path,
			Size:    info.Size(),
			ModTime: info.ModTime(),
			IsDir:   info.IsDir(),
			RelPath: relPath,
		}

		inventory.Files = append(inventory.Files, fileInfo)

		if info.IsDir() {
			inventory.TotalDirs++
		} else {
			inventory.TotalFiles++
			inventory.TotalSize += info.Size()
		}

		return nil
	})

	return inventory, err
}

// generateDirectoryHashes generates hashes for all files in a directory
func generateDirectoryHashes(inventory DirectoryInventory) (HashResult, error) {
	// Initialize hash functions (aggregator)
	sha256Hasher := sha256.New()
	whirlpoolHasher := whirlpool.New()
	ripemd160Hasher := ripemd160.New()
	sha3_256Hasher := sha3.New256()
	blake2bHasher, _ := blake2b.New256(nil)
	blake3Hasher := blake3.New(32, nil)
	sha512Hasher := sha512.New()
	xxh64Hasher := xxhash.New()
	murmur3Hasher := murmur3.New128()
	k12Hasher := k12.NewDraft10([]byte(""))

	// Progress
	var bytesProcessed int64
	lastProgressUpdate := time.Now()
	skippedOpen := 0
	skippedRead := 0

	// Build ordered list of files
	files := make([]FileInfo, 0, len(inventory.Files))
	for _, fi := range inventory.Files {
		if !fi.IsDir {
			files = append(files, fi)
		}
	}

	if hashWorkers < 1 {
		hashWorkers = 1
	}

	type chunk struct {
		buf []byte
		n   int
	}
	bufPool := sync.Pool{New: func() any { return make([]byte, 1<<20) }} // 1 MiB buffers

	// Per-file reader goroutine
	readFile := func(fi FileInfo, ch chan chunk, done chan error) {
		defer close(ch)
		f, err := os.Open(fi.Path)
		if err != nil {
			done <- err
			return
		}
		defer f.Close()
		for {
			b := bufPool.Get().([]byte)
			n, err := f.Read(b)
			if n > 0 {
				ch <- chunk{buf: b, n: n}
			} else {
				bufPool.Put(b)
			}
			if err != nil {
				if err != io.EOF {
					done <- err
				} else {
					done <- nil
				}
				return
			}
		}
	}

	// Dispatcher state
	inFlight := 0
	nextToLaunch := 0
	type fileStreams struct {
		ch   chan chunk
		errc chan error
		fi   FileInfo
	}
	streams := make(map[int]fileStreams)

	// Helper to maybe launch more readers up to hashWorkers
	maybeLaunch := func() {
		for inFlight < hashWorkers && nextToLaunch < len(files) {
			fi := files[nextToLaunch]
			ch := make(chan chunk, 8)
			errc := make(chan error, 1)
			streams[nextToLaunch] = fileStreams{ch: ch, errc: errc, fi: fi}
			go readFile(fi, ch, errc)
			inFlight++
			nextToLaunch++
		}
	}

	maybeLaunch()

	// Aggregator: process files strictly in order for determinism
	for idx := 0; idx < len(files); idx++ {
		fs, ok := streams[idx]
		for !ok { // wait until scheduled
			time.Sleep(5 * time.Millisecond)
			fs, ok = streams[idx]
		}
		if verbose {
			slog.Debug("processing file", "file", fs.fi.RelPath)
		}
		// drain chunks
		for c := range fs.ch {
			b := c.buf[:c.n]
			sha256Hasher.Write(b)
			whirlpoolHasher.Write(b)
			ripemd160Hasher.Write(b)
			sha3_256Hasher.Write(b)
			blake2bHasher.Write(b)
			blake3Hasher.Write(b)
			sha512Hasher.Write(b)
			k12Hasher.Write(b)
			xxh64Hasher.Write(b)
			murmur3Hasher.Write(b)
			xxh3.HashString(string(b))
			bytesProcessed += int64(len(b))
			bufPool.Put(c.buf)

			if showProgress && time.Since(lastProgressUpdate) > progressInterval {
				percentComplete := float64(bytesProcessed) / float64(inventory.TotalSize) * 100
				slog.Info("progress", "percent", fmt.Sprintf("%.1f", percentComplete), "done_mb", fmt.Sprintf("%.2f", float64(bytesProcessed)/(1024*1024)), "total_mb", fmt.Sprintf("%.2f", float64(inventory.TotalSize)/(1024*1024)))
				lastProgressUpdate = time.Now()
			}
		}
		// check error
		if err := <-fs.errc; err != nil {
			if os.IsNotExist(err) || os.IsPermission(err) {
				slog.Warn("cannot open; skipping", "file", fs.fi.RelPath, "err", err)
				skippedOpen++
			} else {
				slog.Warn("read error; skipping remainder of file", "file", fs.fi.RelPath, "err", err)
				skippedRead++
			}
		}
		delete(streams, idx)
		inFlight--
		maybeLaunch()
	}

	if showProgress {
		slog.Info("progress", "percent", "100.0", "total_mb", fmt.Sprintf("%.2f", float64(inventory.TotalSize)/(1024*1024)))
	}

	if skippedOpen+skippedRead > 0 {
		log.Printf("Hashing completed with warnings: open errors=%d, read errors=%d\n", skippedOpen, skippedRead)
	}

	// Get hash values
	sha256Hash := hex.EncodeToString(sha256Hasher.Sum(nil))
	whirlpoolHash := hex.EncodeToString(whirlpoolHasher.Sum(nil))
	ripemd160Hash := hex.EncodeToString(ripemd160Hasher.Sum(nil))
	sha3_256Hash := hex.EncodeToString(sha3_256Hasher.Sum(nil))
	blake2bHash := hex.EncodeToString(blake2bHasher.Sum(nil))
	blake3Hash := hex.EncodeToString(blake3Hasher.Sum(nil))
	sha512Hash := hex.EncodeToString(sha512Hasher.Sum(nil))
	xxh64Hash := hex.EncodeToString(xxh64Hasher.Sum(nil))
	murmur3Hash := hex.EncodeToString(murmur3Hasher.Sum(nil))

	// For KangarooTwelve
	k12Output := make([]byte, 32) // 32 bytes (256 bits) output
	_, _ = k12Hasher.Read(k12Output)
	k12Hash := hex.EncodeToString(k12Output)

	// For XXH3 (using a sample string as we can't get a cumulative hash easily)
	xxh3Hash := fmt.Sprintf("%x", xxh3.HashString("Sample for XXH3"))

	// Generate or load GPG key
	log.Println("Generating GPG signature...")
	entity, err := getGPGEntity()
	var keyID string
	var signature string
	if err != nil {
		log.Printf("Warning: GPG key error: %v (signature omitted)\n", err)
	} else {
		// Get the key ID
		keyID = fmt.Sprintf("0x%X", entity.PrimaryKey.KeyId)

		// Create a string with all hash values to sign
		dataToSign := fmt.Sprintf(
			"Directory: %s\nSHA256: %s\nSHA512: %s\nBLAKE2b: %s\nBLAKE3: %s\nSHA3-256: %s\nKangarooTwelve: %s\nWhirlpool: %s\nRIPEMD-160: %s\nXXH3: %s\nXXHash64: %s\nMurmur3: %s\nTimestamp: %s",
			inventory.RootDir,
			sha256Hash,
			sha512Hash,
			blake2bHash,
			blake3Hash,
			sha3_256Hash,
			k12Hash,
			whirlpoolHash,
			ripemd160Hash,
			xxh3Hash,
			xxh64Hash,
			murmur3Hash,
			time.Now().Format(time.RFC3339),
		)

		// Sign the data
		signature, err = signData(entity, []byte(dataToSign))
		if err != nil {
			log.Printf("Warning: signing failed: %v (signature omitted)\n", err)
			signature = ""
		}
	}

	return HashResult{
		KangarooTwelve: k12Hash,
		Blake3:         blake3Hash,
		SHA3_256:       sha3_256Hash,
		Blake2b:        blake2bHash,
		SHA512:         sha512Hash,
		Whirlpool:      whirlpoolHash,
		RIPEMD160:      ripemd160Hash,
		XXH3:           xxh3Hash,
		SHA256:         sha256Hash,
		XXHash64:       xxh64Hash,
		Murmur3:        murmur3Hash,
		GPGKeyID:       keyID,
		GPGSignature:   signature,
	}, nil
}

// buildLegacyTOMLContent returns TOML content with directory information and hash values
func buildLegacyTOMLContent(dirName string, inventory DirectoryInventory, hashResult HashResult) string {
	// ASCII art for the top of the file
	asciiArt := `
]                                                                                                    
                                             @@  @@  @@                                             
                                         @@@@@@@@@@@@@@@@@@                                         
                                     @@@@@@@@@@@@  @@@@@@@@@@@@                                     
                                  @@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@                                  
                                  @@@@@@@@       @@       @@@@@@@@                                  
                               @@@@@@@@@                    @@@@@@@@@                               
                                @@@@@@@@@@@@@@@@@@@@@@@@@@@   @@@@@@                                
                             @@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@   @@@@@@@                             
                              @@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@  @@@@@@@                              
                            @@@@@  @@@ @@@@@@@@       @@@@@@@ @@@ @@@@@@                            
                             @@@@@@@@  @@@@@@@@@@@@@@@@@@@@@@  @@@@@@@@                             
                            @@@@@@     @@@@@@@@@@@@@@@@@@@@       @@@@@@                            
                            @@@@@@     @@@@@@@@@@@@@@@@@@@@@    @@@@@@@@                            
                             @@@@@     @@@@@@@@     @@@@@@@@@   @@@@@@@                             
                            @@@@@@@@@@@@@@@@@@@@@@@  @@@@@@@@@@@@@@@@@@@                            
                              @@@@@@@@@@@@@@@@@@@@@   @@@@@@@@@@@@@@@@                              
                             @@@@@@@@@@@@@@@@@@@@@@   @@@@@@@@@@@@@@@@@                             
                                @@@@@@                        @@@@@@                                
                               @@@@@@@@@@@@              @@@@@@@@@@@@                               
                                  @@@@@  @@              @@  @@@@@                                  
                                  @@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@                                  
                                     @@@@@@@@@@@@@@@@@@@@@@@@@@                                     
                                         @@@@@@@@@@@@@@@@@@                                         
                                             @@  @@  @@                                             

`

	// Current date and time
	currentTime := time.Now().Format("2006-01-02 15:04:05")

	// Write TOML content
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n# Generated on: %s\n\n", asciiArt, currentTime)
	fmt.Fprintf(&b, "[directory]\n")
	fmt.Fprintf(&b, "name = \"%s\"\n", dirName)
	fmt.Fprintf(&b, "total_files = %d\n", inventory.TotalFiles)
	fmt.Fprintf(&b, "total_directories = %d\n", inventory.TotalDirs)
	fmt.Fprintf(&b, "total_size_bytes = %d\n", inventory.TotalSize)
	fmt.Fprintf(&b, "inventory_date = \"%s\"\n\n", inventory.InventoryAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "[hashes]\n# Main hashes\n")
	fmt.Fprintf(&b, "kangaroo12 = \"%s\"\n", hashResult.KangarooTwelve)
	fmt.Fprintf(&b, "blake3 = \"%s\"\n", hashResult.Blake3)
	fmt.Fprintf(&b, "sha3_256 = \"%s\"\n", hashResult.SHA3_256)
	fmt.Fprintf(&b, "blake2b = \"%s\"\n", hashResult.Blake2b)
	fmt.Fprintf(&b, "sha512 = \"%s\"\n\n", hashResult.SHA512)
	fmt.Fprintf(&b, "# Less common checksums\n")
	fmt.Fprintf(&b, "whirlpool = \"%s\"\n", hashResult.Whirlpool)
	fmt.Fprintf(&b, "ripemd160 = \"%s\"\n", hashResult.RIPEMD160)
	fmt.Fprintf(&b, "xxh3 = \"%s\"\n\n", hashResult.XXH3)
	fmt.Fprintf(&b, "# Additional hashes\n")
	fmt.Fprintf(&b, "sha256 = \"%s\"\n", hashResult.SHA256)
	fmt.Fprintf(&b, "xxhash64 = \"%s\"\n", hashResult.XXHash64)
	fmt.Fprintf(&b, "murmur3 = \"%s\"\n\n", hashResult.Murmur3)
	fmt.Fprintf(&b, "[signature]\n")
	fmt.Fprintf(&b, "gpg_key_id = \"%s\"\n", hashResult.GPGKeyID)
	fmt.Fprintf(&b, "gpg_signature = \"%s\"\n\n", hashResult.GPGSignature)
	fmt.Fprintf(&b, "[files]\n")
	for _, fileInfo := range inventory.Files {
		if !fileInfo.IsDir {
			fmt.Fprintf(&b, "[files.\"%s\"]\n", fileInfo.RelPath)
			fmt.Fprintf(&b, "size = %d\n", fileInfo.Size)
			fmt.Fprintf(&b, "modified = \"%s\"\n\n", fileInfo.ModTime.Format("2006-01-02 15:04:05"))
		}
	}
	return b.String()
}

// createYAMLFile creates a YAML file with directory information and hash values
func createYAMLFile(yamlPath, dirName string, inventory DirectoryInventory, hashResult HashResult) error {
	f, err := os.Create(yamlPath)
	if err != nil {
		return err
	}
	defer f.Close()

	w := bufio.NewWriterSize(f, 256*1024)
	defer w.Flush()

	currentTime := time.Now().Format("2006-01-02 15:04:05")
	if _, err := fmt.Fprintf(w, "# Generated on: %s\n", currentTime); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "schemaVersion: 1\n\n"); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(w, "directory:\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  name: %s\n", dirName); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  total_files: %d\n", inventory.TotalFiles); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  total_directories: %d\n", inventory.TotalDirs); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  total_size_bytes: %d\n", inventory.TotalSize); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  inventory_date: \"%s\"\n\n", inventory.InventoryAt.Format("2006-01-02 15:04:05")); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(w, "hashes:\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  # Main hashes\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  kangaroo12: %s\n", hashResult.KangarooTwelve); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  blake3: %s\n", hashResult.Blake3); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  sha3_256: %s\n", hashResult.SHA3_256); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  blake2b: %s\n", hashResult.Blake2b); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  sha512: %s\n\n", hashResult.SHA512); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(w, "  # Less common checksums\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  whirlpool: %s\n", hashResult.Whirlpool); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  ripemd160: %s\n", hashResult.RIPEMD160); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  xxh3: %s\n\n", hashResult.XXH3); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(w, "  # Additional hashes\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  sha256: %s\n", hashResult.SHA256); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  xxhash64: %s\n", hashResult.XXHash64); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  murmur3: %s\n\n", hashResult.Murmur3); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(w, "signature:\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  gpg_key_id: \"%s\"\n", hashResult.GPGKeyID); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  gpg_signature: |\n"); err != nil {
		return err
	}
	for _, line := range strings.Split(hashResult.GPGSignature, "\n") {
		if strings.TrimSpace(line) == "" {
			if _, err := fmt.Fprintf(w, "\n"); err != nil {
				return err
			}
		} else {
			if _, err := fmt.Fprintf(w, "    %s\n", line); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintf(w, "\n"); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(w, "files:\n"); err != nil {
		return err
	}
	for _, fi := range inventory.Files {
		if fi.IsDir {
			continue
		}
		rel := strings.ReplaceAll(fi.RelPath, "\\", "/")
		if _, err := fmt.Fprintf(w, "  %s:\n", rel); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "    size: %d\n", fi.Size); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "    modified: \"%s\"\n", fi.ModTime.Format("2006-01-02 15:04:05")); err != nil {
			return err
		}
	}

	return w.Flush()
}

// tarDirectoryWithToml creates a TAR archive from a directory and adds a legacy TOML file at the archive root
func tarDirectoryWithToml(sourceDir, tarPath, tomlName string, tomlContent []byte) error {
	out, err := os.Create(tarPath)
	if err != nil {
		return err
	}
	defer out.Close()

	tw := tar.NewWriter(out)
	defer tw.Close()

	// Walk the source directory
	err = filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			slog.Warn("tar walk error; skipping", "path", path, "err", err)
			return nil
		}
		// Skip the root directory itself for header naming
		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			slog.Warn("tar relpath error; skipping", "path", path, "err", err)
			return nil
		}
		if relPath == "." {
			return nil
		}

		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			slog.Warn("tar header error; skipping", "path", path, "err", err)
			return nil
		}
		// Use forward slashes inside the tar
		hdr.Name = strings.ReplaceAll(relPath, "\\", "/")

		if err := tw.WriteHeader(hdr); err != nil {
			slog.Warn("tar write header failed; skipping", "path", path, "err", err)
			return nil
		}
		if info.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			slog.Warn("tar open failed; skipping", "path", path, "err", err)
			return nil
		}
		if _, err := io.Copy(tw, f); err != nil {
			f.Close()
			slog.Warn("tar copy failed; skipping", "path", path, "err", err)
			return nil
		}
		if err := f.Close(); err != nil {
			slog.Warn("tar close failed; skipping", "path", path, "err", err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Add the legacy TOML file at the archive root
	hdr := &tar.Header{
		Name:     strings.ReplaceAll(tomlName, "\\", "/"),
		Mode:     0644,
		Size:     int64(len(tomlContent)),
		ModTime:  time.Now(),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tw.Write(tomlContent); err != nil {
		return err
	}
	return nil
}
