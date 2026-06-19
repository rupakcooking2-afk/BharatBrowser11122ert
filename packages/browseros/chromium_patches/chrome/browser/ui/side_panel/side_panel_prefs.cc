diff --git a/chrome/browser/ui/side_panel/side_panel_prefs.cc b/chrome/browser/ui/side_panel/side_panel_prefs.cc
index 81d36a90837aa..66f30cf53da5b 100644
--- a/chrome/browser/ui/side_panel/side_panel_prefs.cc
+++ b/chrome/browser/ui/side_panel/side_panel_prefs.cc
@@ -13,6 +13,22 @@
 
 namespace side_panel_prefs {
 
+namespace {
+
+constexpr char kThirdPartyLlmProvidersPref[] =
+    "browseros.third_party_llm.providers";
+constexpr char kThirdPartyLlmSelectedProviderPref[] =
+    "browseros.third_party_llm.selected_provider";
+constexpr char kClashOfGptsPaneProvidersPref[] =
+    "browseros.clash_of_gpts.pane_providers";
+constexpr char kClashOfGptsLastUrlsPref[] =
+    "browseros.clash_of_gpts.last_urls";
+constexpr char kClashOfGptsPaneCountPref[] =
+    "browseros.clash_of_gpts.pane_count";
+constexpr int kDefaultClashOfGptsPaneCount = 3;
+
+}  // namespace
+
 void RegisterProfilePrefs(user_prefs::PrefRegistrySyncable* registry) {
 // TODO(crbug.com/489780965): Move policies over as features are implemented.
 #if !BUILDFLAG(IS_ANDROID)
@@ -24,6 +40,18 @@ void RegisterProfilePrefs(user_prefs::PrefRegistrySyncable* registry) {
                                 !base::i18n::IsRTL());
   registry->RegisterBooleanPref(prefs::kGoogleSearchSidePanelEnabled, true);
   registry->RegisterDictionaryPref(prefs::kSidePanelIdToWidth);
+
+  if (base::FeatureList::IsEnabled(features::kThirdPartyLlmPanel)) {
+    registry->RegisterListPref(kThirdPartyLlmProvidersPref);
+    registry->RegisterIntegerPref(kThirdPartyLlmSelectedProviderPref, 0);
+  }
+
+  if (base::FeatureList::IsEnabled(features::kClashOfGpts)) {
+    registry->RegisterListPref(kClashOfGptsPaneProvidersPref);
+    registry->RegisterDictionaryPref(kClashOfGptsLastUrlsPref);
+    registry->RegisterIntegerPref(kClashOfGptsPaneCountPref,
+                                  kDefaultClashOfGptsPaneCount);
+  }
 #endif
 }
 
