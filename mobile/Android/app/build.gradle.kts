plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.compose")
}


fun versionCodeFromTag(tag: String?): Int {
    if (tag.isNullOrBlank() || tag == "dev") return 1
    if (Regex("""^[0-9a-f]{7,40}(-dirty)?$""").matches(tag)) return 1
    val match = Regex("""^v(\d+)\.(\d+)\.(\d+)(-[A-Za-z0-9.-]+)?$""").matchEntire(tag)
        ?: throw GradleException(
            "versionName '$tag' does not match vMAJOR.MINOR.PATCH[-suffix]"
        )
    val (major, minor, patch) = match.destructured
    val minorN = minor.toInt()
    val patchN = patch.toInt()
    if (minorN >= 1000 || patchN >= 1000) {
        throw GradleException(
            "versionName '$tag': MINOR and PATCH must each be under 1000"
        )
    }
    return major.toInt() * 1_000_000 + minorN * 1_000 + patchN
}

android {
    namespace = "io.haoma.calculator"
    compileSdk = 35

    defaultConfig {
        applicationId = "io.haoma.calculator"
        minSdk = 26
        targetSdk = 35
        
        
        versionName = (findProperty("versionName") as String?)?.takeIf { it.isNotBlank() } ?: "dev"
        versionCode = versionCodeFromTag(versionName)
    }

    
    val releaseKeystorePath = System.getenv("HAOMA_RELEASE_KEYSTORE")
    if (!releaseKeystorePath.isNullOrBlank()) {
        signingConfigs {
            create("release") {
                storeFile = file(releaseKeystorePath)
                storePassword = System.getenv("HAOMA_RELEASE_STORE_PASSWORD")
                keyAlias = System.getenv("HAOMA_RELEASE_KEY_ALIAS") ?: "haoma"
                keyPassword = System.getenv("HAOMA_RELEASE_KEY_PASSWORD")
            }
        }
    }

    buildTypes {
        debug {
            isMinifyEnabled = false
        }
        release {
            isMinifyEnabled = false
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro",
            )
            if (!releaseKeystorePath.isNullOrBlank()) {
                signingConfig = signingConfigs.getByName("release")
            }
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }

    buildFeatures {
        compose = true
        buildConfig = true
    }

    
    sourceSets["main"].jniLibs.srcDirs("build/go-bins", "src/main/jniLibs")

    
    packaging {
        jniLibs {
            useLegacyPackaging = true
        }
    }
}

dependencies {
    implementation(project(":disguise"))
    implementation(project(":disguise-calculator"))

    
    implementation("androidx.core:core-ktx:1.13.1")

    val composeBom = platform("androidx.compose:compose-bom:2024.10.01")
    implementation(composeBom)
    implementation("androidx.activity:activity-compose:1.9.3")
    implementation("androidx.lifecycle:lifecycle-runtime-compose:2.8.7")
    implementation("androidx.lifecycle:lifecycle-process:2.8.7")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.8.1")
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.ui:ui-tooling-preview")
    implementation("androidx.compose.material3:material3")
    debugImplementation("androidx.compose.ui:ui-tooling")

    
    implementation("androidx.emoji2:emoji2-emojipicker:1.4.0")

    
    implementation("io.coil-kt:coil-compose:2.7.0")

    
    implementation("com.journeyapps:zxing-android-embedded:4.3.0")

    
    implementation("com.squareup.okhttp3:okhttp:4.12.0")

    testImplementation("junit:junit:4.13.2")
    testImplementation("org.jetbrains.kotlinx:kotlinx-coroutines-test:1.8.1")
    
    
    testImplementation("org.json:json:20231013")
}


tasks.withType<Test>().configureEach {
    val testTmp = layout.buildDirectory.dir("test-tmp").get().asFile
    systemProperty("java.io.tmpdir", testTmp.absolutePath)
    doFirst { testTmp.mkdirs() }
}
