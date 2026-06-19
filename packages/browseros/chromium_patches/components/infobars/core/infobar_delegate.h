diff --git a/components/infobars/core/infobar_delegate.h b/components/infobars/core/infobar_delegate.h
index 5fe307d621193..d7731d7d9acad 100644
--- a/components/infobars/core/infobar_delegate.h
+++ b/components/infobars/core/infobar_delegate.h
@@ -216,6 +216,8 @@ class InfoBarDelegate {
     AUTOFILL_AI_SAVE_ENTITY_INFOBAR_DELEGATE_IOS = 132,
     JS_OPTIMIZATIONS_INFOBAR_DELEGATE = 133,
     WEB_APP_BLOCKED_MIGRATION_INFOBAR_DELEGATE = 134,
+    // BrowserOS: agent installation infobar
+    BROWSEROS_AGENT_INSTALLING_INFOBAR_DELEGATE = 135,
   };
   // LINT.ThenChange(//tools/metrics/histograms/metadata/browser/enums.xml:InfoBarIdentifier)
 
