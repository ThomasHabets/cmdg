# Blueprint: External Rendering Engine Extraction

## Overview
This document outlines the steps to extract the C-based HTML rendering engine and MacOS integration logic from the `cmdg` repository into a standalone tool called `cmdg-render`. This satisfies the maintainer's request to reduce "code liability" in the main project.

## 1. New Repository: `cmdg-render`
A new repository will be created to host the rendering engine.
- **Core Components:**
    - `pkg/render`: The CGO wrapper and the `clib/` directory (moved from `cmdg`).
    - `cmd/cmdg-render`: A simple CLI entry point.
- **CLI Interface:**
    - **Input:** Reads raw HTML from `stdin`.
    - **Arguments:** `--width <int>` (optional, default 80).
    - **Output:** A structured JSON object to `stdout`.
    - **Output Schema:**
      ```json
      {
        "rendered_text": "Formatted text with ##IMG_%d_## placeholders",
        "inline_images": [
          {
            "index": 0,
            "source": "cid:..."
          }
        ]
      }
      ```

## 2. Simplification of `cmdg`
Once the external tool is available, the following steps will be taken in the `cmdg` repository:
- **Deletion:**
    - Remove `pkg/cmdg/clib/` directory entirely.
    - Remove `pkg/cmdg/image_test.go`.
    - Remove all Swift and MacOS-specific C/Go files.
- **Refactoring `pkg/cmdg/message.go`:**
    - Replace the internal `htmlRender()` function.
    - New `htmlRender()` will:
        1. Spawn `cmdg-render --width <termW>`.
        2. Write the HTML body to the process's `stdin`.
        3. Parse the JSON result from `stdout`.
        4. Map the `inline_images` metadata to the existing `InlineImage` struct.

## 3. Deployment and Dependency
- `cmdg` will check for the presence of the `cmdg-render` binary in the user's `$PATH`.
- If missing, it can either:
    - Fall back to basic plaintext (no HTML).
    - Provide a helpful error message: "Please install cmdg-render to view HTML emails and images."

## 4. Implementation Steps
1. **Initialize:** Create the `cmdg-render` project structure.
2. **Migrate:** Copy `clib` and related tests to the new project.
3. **Wrap:** Implement the JSON CLI wrapper in `cmd/cmdg-render/main.go`.
4. **Link:** Update `cmdg` to use the new binary.
5. **Clean:** Execute the deletion of legacy CGO code in `cmdg`.
6. **Verify:** Run the full test suite and manual verification to ensure feature parity.