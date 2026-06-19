diff --git a/ui/views/cocoa/native_widget_mac_ns_window_host.mm b/ui/views/cocoa/native_widget_mac_ns_window_host.mm
index 2914e7149f24f..73fafc062fcd8 100644
--- a/ui/views/cocoa/native_widget_mac_ns_window_host.mm
+++ b/ui/views/cocoa/native_widget_mac_ns_window_host.mm
@@ -492,6 +492,7 @@ void HandleAccelerator(const ui::Accelerator& accelerator,
     window_params->is_translucent =
         params.opacity == Widget::InitParams::WindowOpacity::kTranslucent;
     window_params->is_tooltip = is_tooltip;
+    window_params->is_headless = params.headless;
 
     // macOS likes to put shadows on most things. However, frameless windows
     // (with styleMask = NSWindowStyleMaskBorderless) default to no shadow. So
