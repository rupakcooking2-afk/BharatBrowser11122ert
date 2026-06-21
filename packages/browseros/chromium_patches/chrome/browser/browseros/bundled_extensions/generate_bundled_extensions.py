#!/usr/bin/env python3
# Copyright 2024 The Chromium Authors
# Use of this source code is governed by a BSD-style license that can be
# found in the LICENSE file.

"""Generates bundled extension CRX files for build.

Checks for real CRX files downloaded by the Python build pipeline.
If unavailable, creates placeholders so Ninja does not fail.
"""

import json
import os
import shutil
import sys


def main():
  output_dir = sys.argv[1]

  # Source directory where the Python build pipeline places downloaded CRX files
  script_dir = os.path.dirname(os.path.abspath(__file__))

  # Expected bundled extensions
  extensions = {
    "bflpfmnmnokmjhmgnolecpppdbdophmk": "Agent",
    "adlpneommgkgeanpaekgoaolcpncohkf": "Bug Reporter",
    "nlnihljpboknmfagkikhkdblbedophja": "Controller",
  }

  os.makedirs(output_dir, exist_ok=True)

  manifest = {}
  for ext_id, ext_name in extensions.items():
    src_path = os.path.join(script_dir, f"{ext_id}.crx")
    dst_path = os.path.join(output_dir, f"{ext_id}.crx")

    if os.path.isfile(src_path):
      shutil.copy2(src_path, dst_path)
      file_size = os.path.getsize(dst_path)
    else:
      with open(dst_path, "wb") as f:
        f.write(b"")
      file_size = 0

    manifest[ext_id] = {
      "external_crx": f"{ext_id}.crx",
      "external_version": "0.0.0.0",
    }

  json_path = os.path.join(output_dir, "bundled_extensions.json")
  with open(json_path, "w") as f:
    json.dump(manifest, f, indent=2)
    f.write("\n")


if __name__ == "__main__":
  main()
