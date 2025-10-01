# Archive-Hasher

Archive-Hasher is a tool for generating cryptographic hashes and checksums for entire directories, creating a YAML inventory file, and tarring the directory. It also includes the legacy TOML metadata inside the TAR for compatibility. It's based on the iso-hasher tool but adapted to work with project directories instead of ISO files.

## Features

- Recursively inventories all files in a directory
- Generates multiple cryptographic hashes and checksums:
  - **5 Main Hashes**: KangarooTwelve, BLAKE3, SHA3-256, BLAKE2b, SHA-512
  - **3 Less Common Checksums**: Whirlpool, RIPEMD-160, XXH3
  - **Additional Hashes**: SHA-256, XXHash64, Murmur3
- Creates a detailed YAML file with:
  - Directory information (name, file count, size, etc.)
  - All hash values
  - Complete file inventory with sizes and timestamps
- Automatically creates a TAR archive of the entire directory (includes the legacy .toml metadata file inside)
- Progress reporting for large directories

## Installation

### Prerequisites

- Go 1.16 or later

### Building from Source

1. Clone the repository or download the source code
2. Navigate to the directory containing the source code
3. Initialize the Go module:
   ```
   go mod init dir-hasher
   ```
4. Install dependencies:
   ```
   go mod tidy
   ```
5. Build the executable:
   ```
   go build
   ```

## Usage

```
archive-hasher -dir <directory_path> [options]
```

### Options

- `-dir string`: Directory to hash and tar (required)
- `-verbose`: Enable verbose output
- `-progress`: Show progress when hashing large files (default true)
- `-progress-interval duration`: Interval between progress updates (e.g., `3s`, `30s`, `1m`) — default `3s`
- `-gpgkey string`: Path to GPG private key file (if not provided, a new key will be generated)
- `-out-dir string`: Optional output directory for `.yaml` and `.tar` (default: alongside input directory)
- `-out-prefix string`: Optional filename prefix for outputs (default: directory name)
- `-log-format text|json`: Structured logging format (default: `text`)
- `-log-level debug|info|warn|error`: Logging verbosity (default: `info`). `-verbose` bumps to `debug` unless `-log-level` is set.
- `-hash-workers int`: Number of concurrent file readers used while hashing (default: number of CPUs). Order of aggregation is preserved for deterministic outputs.

### Examples

Basic run:
```
archive-hasher -dir C:\Projects\my-project -verbose
```

Custom progress cadence and outputs to a separate directory with a custom prefix:
```
archive-hasher -dir C:\Projects\my-project \
  -progress-interval 10s \
  -out-dir D:\Rust-Crates\Artifacts \
  -out-prefix my-project-2025-08-23
```
This will write:
- D:\Rust-Crates\Artifacts\my-project-2025-08-23.yaml
- D:\Rust-Crates\Artifacts\my-project-2025-08-23.tar (containing my-project-2025-08-23.toml at archive root)

### Windows PowerShell note about parentheses, spaces, and special characters

PowerShell treats parentheses `()` as expression grouping. If your directory path contains parentheses (or other special characters), PowerShell may evaluate the content inside and pass unintended flags like `-39` to the program. To avoid this, quote or escape the path. Examples:

- Use single quotes (recommended in PowerShell):
  ```powershell
  go run Archive-Hasher.go -dir 'C:\Rust-Crates\crates.io(8-22-25)'
  ```

- Or use double quotes:
  ```powershell
  go run Archive-Hasher.go -dir "C:\Rust-Crates\crates.io(8-22-25)"
  ```

- Or escape the parentheses with backticks (PowerShell escape char):
  ```powershell
  go run Archive-Hasher.go -dir C:\Rust-Crates\crates.io`(8-22-25`)
  ```

- Or use the stop-parsing token `--%` to prevent PowerShell from interpreting anything after it (everything after `--%` is passed verbatim to the program):
  ```powershell
  go run Archive-Hasher.go --% -dir C:\Rust-Crates\crates.io(8-22-25)
  ```

If you’re using CMD.exe instead of PowerShell, quoting with double quotes is sufficient:

```cmd
 go run Archive-Hasher.go -dir "C:\Rust-Crates\crates.io(8-22-25)"
```

This will:
1. Create an inventory of all files in the `C:\Projects\my-project` directory
2. Generate hashes for all files
3. Generate a new GPG key pair and sign the hash data
4. Create a YAML file at `C:\Projects\my-project.yaml`
5. Create a TAR file at `C:\Projects\my-project.tar` (which includes the legacy `my-project.toml` inside the archive)

Note: The output filename prefix no longer contains a trailing hyphen.

### Using an Existing GPG Key

If you want to use an existing GPG key for signing:

```
archive-hasher -dir C:\Projects\my-project -gpgkey C:\path\to\private-key.asc
```

The private key file should be in ASCII-armored format.

## Output

### YAML File Structure

The generated YAML file includes (example schema):

```yaml
# Generated on: YYYY-MM-DD HH:MM:SS
schemaVersion: 1

directory:
  name: directory-name
  total_files: 123
  total_directories: 45
  total_size_bytes: 67890123
  inventory_date: "YYYY-MM-DD HH:MM:SS"

hashes:
  # Main hashes
  kangaroo12: hash-value
  blake3: hash-value
  sha3_256: hash-value
  blake2b: hash-value
  sha512: hash-value

  # Less common checksums
  whirlpool: hash-value
  ripemd160: hash-value
  xxh3: hash-value

  # Additional hashes
  sha256: hash-value
  xxhash64: hash-value
  murmur3: hash-value

signature:
  gpg_key_id: "0xABCDEF1234567890"
  gpg_signature: |
    -----BEGIN PGP SIGNATURE-----
    Version: GnuPG v2

    iQEzBAABCAAdFiEEq1VR5N4kR/VF4xOuHSQEBQL8TTQFAmXXXXXACgkQHSQEBQL8
    TTQzQQf/XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
    XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
    XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
    XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
    =XXXX
    -----END PGP SIGNATURE-----

files:
  relative/path/to/file1:
    size: 12345
    modified: "YYYY-MM-DD HH:MM:SS"
  relative/path/to/file2:
    size: 67890
    modified: "YYYY-MM-DD HH:MM:SS"
```

### TAR File

The TAR file contains all files and directories from the source directory, preserving the directory structure, and also includes the legacy `.toml` metadata file for compatibility.

## Dependencies

- github.com/ProtonMail/go-crypto/openpgp - Maintained OpenPGP fork for signatures (replaces deprecated golang.org/x/crypto/openpgp)
- github.com/cloudflare/circl/xof/k12 - For KangarooTwelve hash
- github.com/jzelinskie/whirlpool - For Whirlpool hash
- golang.org/x/crypto/blake2b - For BLAKE2b hash
- golang.org/x/crypto/ripemd160 - For RIPEMD-160 hash
- golang.org/x/crypto/sha3 - For SHA3-256 hash
- lukechampine.com/blake3 - For BLAKE3 hash
- github.com/zeebo/xxh3 - For XXH3 hash
- github.com/cespare/xxhash/v2 - For XXHash64
- github.com/spaolacci/murmur3 - For Murmur3 hash
- archive/tar - For TAR file creation

## License

This project is licensed under the MIT License - see the LICENSE file for details.
