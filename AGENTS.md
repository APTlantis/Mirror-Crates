# Mirroring Rust Crates.io

- This is designed to be a highly efficient way to mirror crates.io for use in air-gapped environments.
- It uses a python script to download the index and all crate files, a go script to download the files, and another go script to pair metadata with the files. As well as an archive hasher.

- This project is the most recent and fastest way to do this on github, so we want to serve it there in a very polished and efficient way.
- Ideally we add a GUI to it as well.

## Agent Handoff (2025-10-03)
- Refactor in progress: downloader and sidecar logic moved under internal/ with new CLIs in cmd/.
- Local Go 1.25.1 toolchain unpacked in .tools/go; go test ./... now green using that binary.
- README build instructions still reference old paths; update pending once workspace is relaunched under WSL.
- Next: finish documentation refresh, run gofmt with native Go toolchain, and continue GUI planning after relocating repo under WSL home.
