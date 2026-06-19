diff --git a/components/remote_cocoa/app_shim/native_widget_ns_window_bridge.mm b/components/remote_cocoa/app_shim/native_widget_ns_window_bridge.mm
index d58c5eff9f8fb..761a6cd68f271 100644
--- a/components/remote_cocoa/app_shim/native_widget_ns_window_bridge.mm
+++ b/components/remote_cocoa/app_shim/native_widget_ns_window_bridge.mm
@@ -555,7 +555,7 @@ NSUInteger CountBridgedWindows(NSArray* child_windows) {
   is_translucent_window_ = params->is_translucent;
   pending_restoration_data_ = params->state_restoration_data.Clone();
 
-  if (display::Screen::Get()->IsHeadless()) {
+  if (params->is_headless || display::Screen::Get()->IsHeadless()) {
     [window_ setIsHeadless:YES];
   }
 
