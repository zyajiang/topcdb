# TopcDB

TopcDB is a two-phase compactable lossy compression framework for LSM-tree-based time-series databases.

This repository contains our implementation of **TopcDB on top of Prometheus**, focusing on error-bounded compression for time-series workloads with compaction and out-of-order data.

## Overview

Lossy compression is attractive for time-series databases because it can significantly reduce storage cost while preserving trend information under a bounded error. However, existing lossy compressors are not well aligned with the storage behavior of LSM-tree-based TSDBs.

TopcDB is designed to address two practical issues:

1. **Error accumulation during compaction**  
   Traditional lossy compression may introduce additional error every time compressed chunks are decompressed, merged, and recompressed during compaction.

2. **Poor compression on small chunks**  
   Time-series databases usually compress small in-memory chunks, where many state-of-the-art lossy compressors become less effective.

TopcDB introduces a **two-phase compression architecture** to solve both problems while remaining compatible with the compaction workflow of Prometheus-style storage engines.

## Key Ideas

### Phase 1: Compression for in-memory and lower-level chunks
Phase 1 is designed for small chunks and hot data. It combines:

- **Linear-scale quantization** for error-bounded float-to-integer conversion
- A **merge-friendly Lorenzo predictor**
- **Simple8b** bit packing for efficient encoding of small integer residuals

This phase provides strong compression even when chunk sizes are small.

### Phase 2: Compression for compaction and persistent storage
Phase 2 is used when chunks are compacted and merged into larger blocks. It performs:

- Decoding of quantized values
- Chronological merging
- Re-encoding with **entropy coding**
- Level-aware chunk resizing for better compression at higher storage levels

This design avoids repeated lossy re-quantization during compaction and improves compression efficiency for cold data.

### Adaptive chunk sizing
TopcDB increases chunk sizes across storage levels to match tiered compaction behavior. This improves compression density while keeping the design compatible with the underlying LSM-tree workflow.

## Highlights

According to the paper, TopcDB achieves:

- **4.1×–8.3× higher compression ratio than Prometheus**
- **147% higher write throughput than Prometheus**
- **1.1×–2.8× higher compression ratio** than state-of-the-art lossy compressors on in-order data
- **1.1×–1.4× higher compression ratio** than state-of-the-art lossy compressors on out-of-order data
- **8%–13% higher compression ratio** than standalone QSimple8b through adaptive chunk sizing and selective encoding

## Repository Structure

This repository is based on the Prometheus codebase. The main directories include:

- `cmd/` – executable entry points
- `tsdb/` – TSDB-related implementation
- `storage/` – storage layer components
- `web/` – web/UI components
- `documentation/` and `docs/` – documentation assets

If you are exploring the TopcDB implementation, start from the TSDB and storage-related components.

## Getting Started

### Prerequisites

To build from source, make sure you have:

- Go
- Node.js
- npm
- Make

### Clone the repository

```bash
git clone https://github.com/zyajiang/topcdb.git
cd topcdb
git checkout topcdb-dev
```

## Build

```bash
make build
```

## Run

```bash
./prometheus --config.file=your_config.yml
```

## Citation

If you use this repository in your research, please cite:
```bibtex
@misc{jiang2025topcdb,
  title  = {TopcDB: Two-Phase Compactable Lossy Compression for Timeseries Databases},
  author = {Zijian Jiang and Peiquan Jin},
  year   = {2025}
}
```

## Acknowledgement

This project is implemented on top of the Prometheus codebase. We thank the Prometheus community and maintainers for providing the foundation that made this work possible.