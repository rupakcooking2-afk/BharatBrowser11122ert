diff --git a/ui/views/widget/widget.h b/ui/views/widget/widget.h
index 93a47570d4854..04de4cc102ba5 100644
--- a/ui/views/widget/widget.h
+++ b/ui/views/widget/widget.h
@@ -501,6 +501,14 @@ class VIEWS_EXPORT Widget : public internal::NativeWidgetDelegate,
     // If true then the widget uses software compositing.
     bool force_software_compositing = false;
 
+    // When true, the native widget is created without OS-visible presence
+    // (no taskbar entry, no Mission Control, no Alt-Tab) while keeping the
+    // compositor path alive. Honored per-platform by native widget
+    // implementations (Mac swizzler, Windows WS_EX_TOOLWINDOW + never-show,
+    // X11 unmapped-with-skip-taskbar). Decided at construction; never
+    // transitions.
+    bool headless = false;
+
     // If set, the window size will follow the content preferred size.
     bool autosize = false;
 
