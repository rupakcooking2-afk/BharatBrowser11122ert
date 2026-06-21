diff --git a/chrome/browser/browseros/server/generate_server_resources.py b/chrome/browser/browseros/server/generate_server_resources.py
new file mode 100755
index 0000000000000..0000000000000000000000000000000000000000
--- /dev/null
+++ b/chrome/browser/browseros/server/generate_server_resources.py
@@ -0,0 +1,48 @@
+#!/usr/bin/env python3
+"""Generates placeholder BrowserOS server resources for GN build.
+
+Creates placeholder binary files when real resources are not available,
+allowing the build to succeed without R2/cloud storage access for the
+server binaries. When real resources exist (non-empty), they are preserved.
+"""
+
+import os
+import stat
+import sys
+
+
+def main():
+    output_dir = sys.argv[1]
+    binary_name = sys.argv[2]
+
+    files_to_create = [
+        f"bin/{binary_name}",
+        "bin/codex",
+        "bin/claude",
+    ]
+
+    os.makedirs(output_dir, exist_ok=True)
+    missing = []
+
+    for rel_path in files_to_create:
+        full_path = os.path.join(output_dir, rel_path)
+        parent = os.path.dirname(full_path)
+        os.makedirs(parent, exist_ok=True)
+
+        if os.path.isfile(full_path) and os.path.getsize(full_path) > 0:
+            continue
+
+        with open(full_path, "w") as f:
+            f.write("")
+        os.chmod(full_path, stat.S_IRUSR | stat.S_IWUSR | stat.S_IXUSR |
+                 stat.S_IRGRP | stat.S_IXGRP | stat.S_IROTH | stat.S_IXOTH)
+        missing.append(rel_path)
+
+    if missing:
+        print(f"generate_server_resources: created placeholder(s): {missing}")
+
+    return 0
+
+
+if __name__ == "__main__":
+    sys.exit(main())
