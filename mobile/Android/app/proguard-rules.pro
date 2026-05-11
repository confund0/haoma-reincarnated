# Strip every android.util.Log call from the release APK at compile time.
# Belt-and-braces for feedback_android_no_logcat.md — even if a Log call slips
# in, the shrinker removes it before it can reach logcat.
-assumenosideeffects class android.util.Log {
    public static *** v(...);
    public static *** d(...);
    public static *** i(...);
    public static *** w(...);
    public static *** e(...);
    public static *** wtf(...);
}
