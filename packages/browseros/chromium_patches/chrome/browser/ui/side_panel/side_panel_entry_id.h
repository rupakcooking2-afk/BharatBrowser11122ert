diff --git a/chrome/browser/ui/side_panel/side_panel_entry_id.h b/chrome/browser/ui/side_panel/side_panel_entry_id.h
index b089b5e276bab..f4124505c713b 100644
--- a/chrome/browser/ui/side_panel/side_panel_entry_id.h
+++ b/chrome/browser/ui/side_panel/side_panel_entry_id.h
@@ -42,6 +42,8 @@
   V(kGlic, kActionSidePanelShowGlic, "Glic")                                  \
   V(kTabsFromOtherDevices, kActionSidePanelShowTabsFromOtherDevices,          \
     "TabsFromOtherDevices")                                                   \
+  V(kThirdPartyLlm, kActionSidePanelShowThirdPartyLlm, "ThirdPartyLlm")       \
+  V(kClashOfGpts, kActionSidePanelShowClashOfGpts, "ClashOfGpts")             \
   /* Extensions (nothing more should be added below here) */                  \
   V(kExtension, std::nullopt, "Extension")
 
