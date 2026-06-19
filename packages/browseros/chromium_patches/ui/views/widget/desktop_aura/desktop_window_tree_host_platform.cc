diff --git a/ui/views/widget/desktop_aura/desktop_window_tree_host_platform.cc b/ui/views/widget/desktop_aura/desktop_window_tree_host_platform.cc
index 942e685096b85..618213c82661d 100644
--- a/ui/views/widget/desktop_aura/desktop_window_tree_host_platform.cc
+++ b/ui/views/widget/desktop_aura/desktop_window_tree_host_platform.cc
@@ -142,6 +142,7 @@ ui::PlatformWindowInitProperties ConvertWidgetInitParamsToInitProperties(
   properties.workspace = params.workspace;
   properties.opacity = GetPlatformWindowOpacity(params.opacity);
   properties.shadow_type = GetPlatformWindowShadowType(params.shadow_type);
+  properties.headless = params.headless;
 
   if (params.parent && params.parent->GetHost()) {
     properties.parent_widget = params.parent->GetHost()->GetAcceleratedWidget();
