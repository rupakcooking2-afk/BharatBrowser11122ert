diff --git a/chrome/browser/browseros/bundled_extensions/generate_bundled_extensions.py b/chrome/browser/browseros/bundled_extensions/generate_bundled_extensions.py
new file mode 100755
index 0000000000000..0000000
--- /dev/null
+++ b/chrome/browser/browseros/bundled_extensions/generate_bundled_extensions.py
@@ -0,0 +1,57 @@
+#!/usr/bin/env python3
+# Copyright 2024 The Chromium Authors
+# Use of this source code is governed by a BSD-style license that can be
+# found in the LICENSE file.
+
+"""Generates bundled extension CRX files for build.
+
+Creates placeholder CRX files when real extensions are not available.
+If real CRX files exist (downloaded by build pipeline), uses those instead.
+"""
+
+import json
+import os
+import shutil
+import sys
+
+
+def main():
+  output_dir = sys.argv[1]
+  script_dir = os.path.dirname(os.path.abspath(__file__))
+
+  extensions = [
+    "bflpfmnmnokmjhmgnolecpppdbdophmk",
+    "adlpneommgkgeanpaekgoaolcpncohkf",
+    "nlnihljpboknmfagkikhkdblbedophja",
+  ]
+
+  os.makedirs(output_dir, exist_ok=True)
+
+  manifest = {}
+  for ext_id in extensions:
+    src_path = os.path.join(script_dir, f"{ext_id}.crx")
+    dst_path = os.path.join(output_dir, f"{ext_id}.crx")
+
+    if os.path.isfile(src_path):
+      shutil.copy2(src_path, dst_path)
+    else:
+      with open(dst_path, "wb") as f:
+        f.write(b"")
+
+    manifest[ext_id] = {
+      "external_crx": f"{ext_id}.crx",
+      "external_version": "0.0.0.0",
+    }
+
+  json_path = os.path.join(output_dir, "bundled_extensions.json")
+  with open(json_path, "w") as f:
+    json.dump(manifest, f, indent=2)
+    f.write("\n")
+
+  missing = [e for e in extensions if not os.path.isfile(os.path.join(script_dir, f"{e}.crx"))]
+  if missing:
+    print(f"generate_bundled_extensions: WARNING - no real CRX found for {missing}, using placeholders")
+
+
+if __name__ == "__main__":
+  main()
