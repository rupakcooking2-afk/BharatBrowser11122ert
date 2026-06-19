diff --git a/components/content_settings/core/browser/cookie_settings.cc b/components/content_settings/core/browser/cookie_settings.cc
index 1bb51ad9d1aa0..e0e3c8375ce08 100644
--- a/components/content_settings/core/browser/cookie_settings.cc
+++ b/components/content_settings/core/browser/cookie_settings.cc
@@ -83,7 +83,7 @@ void CookieSettings::RegisterProfilePrefs(
     user_prefs::PrefRegistrySyncable* registry) {
   registry->RegisterIntegerPref(
       prefs::kCookieControlsMode,
-      static_cast<int>(CookieControlsMode::kIncognitoOnly),
+      static_cast<int>(CookieControlsMode::kBlockThirdParty),
       user_prefs::PrefRegistrySyncable::SYNCABLE_PREF);
 }
 
